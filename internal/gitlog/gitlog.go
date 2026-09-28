// Package gitlog runs the git CLI and parses its output.
package gitlog

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// Commit is one commit with its per-file line counts.
type Commit struct {
	Hash        string
	Parents     []string
	AuthorName  string
	AuthorEmail string
	AuthorTime  int64
	CommitTime  int64
	Subject     string
	Files       []FileChange
}

// FileChange is one numstat line. Binary files have zero line counts.
type FileChange struct {
	Path       string
	Insertions int
	Deletions  int
	Binary     bool
}

// Ref is a local or remote branch.
type Ref struct {
	Name   string `json:"name"`
	Tip    string `json:"tip"`
	Remote bool   `json:"remote"`
}

func git(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	args = append([]string{"-C", dir, "-c", "core.quotePath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[4], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func lines(out []byte) []string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// TopLevel returns the root of the work tree that contains dir.
func TopLevel(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, nil, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(string(out)), err
}

// GitDir returns the absolute .git directory of the repository.
func GitDir(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, nil, "rev-parse", "--absolute-git-dir")
	return strings.TrimSpace(string(out)), err
}

// Head returns the current branch name, or "" when HEAD is detached.
func Head(ctx context.Context, dir string) string {
	out, _ := git(ctx, dir, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	return strings.TrimSpace(string(out))
}

// RevListAll returns every commit reachable from any ref.
func RevListAll(ctx context.Context, dir string) ([]string, error) {
	out, err := git(ctx, dir, nil, "rev-list", "--all")
	return lines(out), err
}

// RevList returns every commit reachable from rev.
func RevList(ctx context.Context, dir, rev string) ([]string, error) {
	out, err := git(ctx, dir, nil, "rev-list", rev, "--")
	return lines(out), err
}

// Refs lists local and remote branches.
func Refs(ctx context.Context, dir string) ([]Ref, error) {
	out, err := git(ctx, dir, nil, "for-each-ref",
		"--format=%(refname)%1f%(refname:short)%1f%(objectname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, l := range lines(out) {
		f := strings.Split(l, "\x1f")
		if len(f) != 3 || strings.HasSuffix(f[0], "/HEAD") {
			continue
		}
		refs = append(refs, Ref{Name: f[1], Tip: f[2], Remote: strings.HasPrefix(f[0], "refs/remotes/")})
	}
	return refs, nil
}

// TrackedFiles returns the paths present at HEAD.
func TrackedFiles(ctx context.Context, dir string) (map[string]bool, error) {
	out, err := git(ctx, dir, nil, "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	files := make(map[string]bool)
	for _, l := range lines(out) {
		files[l] = true
	}
	return files, nil
}

// Show returns the patch of a commit, cut at limit bytes.
func Show(ctx context.Context, dir, hash string, limit int) (patch string, truncated bool, err error) {
	out, err := git(ctx, dir, nil, "show", "--format=", "--patch", "--no-color", "--first-parent", hash, "--")
	if err != nil {
		return "", false, err
	}
	if len(out) > limit {
		return string(out[:limit]), true, nil
	}
	return string(out), false, nil
}

const logFormat = "--format=%x1e%H%x1f%P%x1f%aN%x1f%aE%x1f%at%x1f%ct%x1f%s"

// Log streams the given commits (and only those) to fn.
func Log(ctx context.Context, dir string, hashes []string, fn func(Commit) error) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "-c", "core.quotePath=false",
		"log", "--no-walk=unsorted", "--stdin", "--no-renames", "--numstat", logFormat)
	cmd.Stdin = strings.NewReader(strings.Join(hashes, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	parseErr := parse(stdout, fn)
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	if err := cmd.Wait(); err != nil && parseErr == nil {
		return fmt.Errorf("git log: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseErr
}

// parse reads the output of `git log --numstat` in logFormat.
func parse(r io.Reader, fn func(Commit) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var cur *Commit
	flush := func() error {
		if cur == nil {
			return nil
		}
		c := *cur
		cur = nil
		return fn(c)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "\x1e"):
			if err := flush(); err != nil {
				return err
			}
			c, err := parseHeader(line[1:])
			if err != nil {
				return err
			}
			cur = &c
		case line == "" || cur == nil:
		default:
			if fc, ok := parseNumstat(line); ok {
				cur.Files = append(cur.Files, fc)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

func parseHeader(s string) (Commit, error) {
	f := strings.SplitN(s, "\x1f", 7)
	if len(f) != 7 {
		return Commit{}, fmt.Errorf("unexpected log header %q", s)
	}
	at, _ := strconv.ParseInt(f[4], 10, 64)
	ct, _ := strconv.ParseInt(f[5], 10, 64)
	return Commit{
		Hash: f[0], Parents: strings.Fields(f[1]),
		AuthorName: f[2], AuthorEmail: strings.ToLower(f[3]),
		AuthorTime: at, CommitTime: ct, Subject: f[6],
	}, nil
}

func parseNumstat(line string) (FileChange, bool) {
	f := strings.SplitN(line, "\t", 3)
	if len(f) != 3 {
		return FileChange{}, false
	}
	if f[0] == "-" && f[1] == "-" {
		return FileChange{Path: f[2], Binary: true}, true
	}
	ins, err1 := strconv.Atoi(f[0])
	del, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return FileChange{}, false
	}
	return FileChange{Path: f[2], Insertions: ins, Deletions: del}, true
}
