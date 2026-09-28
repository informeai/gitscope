package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// repo builds a small history:
//
//	main:   c1 (ana, 2026-01-05) ── c2 (bruno, 2026-01-20) ── c4 (ana, 2026-03-02)
//	                            └── feat: c3 (bruno, 2026-02-10)
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(name, email, date, msg string) {
		t.Helper()
		run(nil, "add", "-A")
		env := []string{
			"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
			"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + date,
		}
		run(env, "commit", "-q", "-m", msg)
	}

	run(nil, "init", "-q", "-b", "main")
	write("internal/finance/interest.go", "a\nb\nc\n")
	write("README.md", "hi\n")
	commit("Ana", "ana@example.com", "2026-01-05T10:00:00", "feat: juros")
	write("internal/finance/interest.go", "a\nB\nc\nd\n")
	commit("Bruno", "Bruno@Example.com", "2026-01-20T10:00:00", "fix: arredondamento")
	run(nil, "checkout", "-q", "-b", "feat")
	write("internal/iam/login.go", "x\n")
	commit("Bruno", "bruno@example.com", "2026-02-10T10:00:00", "feat: login")
	run(nil, "checkout", "-q", "main")
	write("README.md", "hello\n")
	commit("Ana", "ana@example.com", "2026-03-02T10:00:00", "docs: readme")
	return dir
}

func unix(date string) int64 {
	t, _ := time.ParseInLocation(time.DateOnly, date, time.Local)
	return t.Unix()
}

func TestStore(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	st, err := Open(filepath.Join(t.TempDir(), "index.db"), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	res, err := st.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 4 || res.Total != 4 {
		t.Fatalf("first sync = %+v, want 4 added", res)
	}
	if res, _ := st.Sync(ctx); res.Added != 0 || res.Removed != 0 {
		t.Fatalf("second sync = %+v, want no changes", res)
	}

	sum, err := st.Summary(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Commits != 4 || sum.Authors != 2 || sum.Files != 3 {
		t.Fatalf("summary = %+v", sum)
	}

	// Emails are case-insensitive: both Bruno commits count as one author.
	authors, _ := st.Authors(ctx, Filter{})
	if len(authors) != 2 || authors[0].Commits != 2 || authors[1].Commits != 2 {
		t.Fatalf("authors = %+v", authors)
	}

	cases := []struct {
		name string
		f    Filter
		want int
	}{
		{"author", Filter{Authors: []string{"BRUNO@example.com"}}, 2},
		{"path prefix", Filter{Path: "internal/fin"}, 2},
		{"period", Filter{Since: unix("2026-01-15"), Until: unix("2026-02-15")}, 2},
		{"subject", Filter{Query: "LOGIN"}, 1},
		{"author and path", Filter{Authors: []string{"ana@example.com"}, Path: "internal/"}, 1},
	}
	for _, c := range cases {
		got, err := st.Summary(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if got.Commits != c.want {
			t.Errorf("%s: %d commits, want %d", c.name, got.Commits, c.want)
		}
	}

	// Path filter restricts line counts to matching files.
	fin, _ := st.Summary(ctx, Filter{Path: "internal/finance"})
	if fin.Insertions != 5 || fin.Deletions != 1 {
		t.Errorf("finance lines = +%d -%d, want +5 -1", fin.Insertions, fin.Deletions)
	}

	// Branch filter: main does not contain the feat commit.
	tip := gitOut(t, dir, "rev-parse", "main")
	if err := st.EnsureTip(ctx, tip); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Summary(ctx, Filter{Tip: tip}); got.Commits != 3 {
		t.Errorf("main has %d commits, want 3", got.Commits)
	}

	months, err := st.Timeline(ctx, Filter{}, "month")
	if err != nil {
		t.Fatal(err)
	}
	if len(months) != 3 || months[0].Commits != 2 || months[1].Commits != 1 {
		t.Errorf("months = %+v", months)
	}
	weeks, _ := st.Timeline(ctx, Filter{}, "week")
	if weeks[0].Start != "2026-01-05" || len(weeks) != 9 {
		t.Errorf("weeks start %s, len %d; want 2026-01-05, 9", weeks[0].Start, len(weeks))
	}

	files, _ := st.Files(ctx, Filter{}, 10)
	if files[0].Path != "README.md" && files[0].Path != "internal/finance/interest.go" || files[0].Changes != 2 {
		t.Errorf("top file = %+v", files[0])
	}

	d, err := st.Commit(ctx, tip[:7])
	if err != nil || d.Hash != tip || d.Subject != "docs: readme" {
		t.Errorf("Commit(%s) = %+v, %v", tip[:7], d, err)
	}

	// Deleting the branch drops its commit on the next sync.
	cmd := exec.Command("git", "branch", "-q", "-D", "feat")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if res, _ := st.Sync(ctx); res.Removed != 1 {
		t.Errorf("sync after branch delete = %+v, want 1 removed", res)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:len(out)-1])
}
