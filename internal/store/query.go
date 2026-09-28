package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Filter narrows every query. Zero values mean "no restriction".
type Filter struct {
	Authors []string // author emails
	Tip     string   // only commits reachable from this commit (see EnsureTip)
	Since   int64    // unix seconds, inclusive
	Until   int64    // unix seconds, exclusive
	Path    string   // path prefix; line counts only cover matching files
	Query   string   // substring of the subject
	Merges  bool     // include merge commits
}

// where returns the conditions on commits c.
func (f Filter) where() (string, []any) {
	conds := []string{"1=1"}
	var args []any
	if !f.Merges {
		conds = append(conds, "c.is_merge = 0")
	}
	if len(f.Authors) > 0 {
		conds = append(conds, "c.author_email IN ("+placeholders(len(f.Authors))+")")
		for _, a := range f.Authors {
			args = append(args, strings.ToLower(a))
		}
	}
	if f.Tip != "" {
		conds = append(conds, "c.hash IN (SELECT hash FROM ref_commits WHERE tip = ?)")
		args = append(args, f.Tip)
	}
	if f.Since > 0 {
		conds = append(conds, "c.author_time >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		conds = append(conds, "c.author_time < ?")
		args = append(args, f.Until)
	}
	if f.Query != "" {
		conds = append(conds, `c.subject LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(f.Query)+"%")
	}
	if f.Path != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM file_changes p WHERE p.hash = c.hash AND substr(p.path, 1, ?) = ?)")
		args = append(args, utf8.RuneCountInString(f.Path), f.Path)
	}
	return strings.Join(conds, " AND "), args
}

// join returns the join from commits c to file_changes fc, restricted to the
// path filter so that line counts only cover matching files.
func (f Filter) join(kind string) (string, []any) {
	j := kind + " JOIN file_changes fc ON fc.hash = c.hash"
	if f.Path == "" {
		return j, nil
	}
	return j + " AND substr(fc.path, 1, ?) = ?", []any{utf8.RuneCountInString(f.Path), f.Path}
}

// sql builds "SELECT <cols> FROM commits c <join> WHERE <filter> <tail>".
func (f Filter) sql(cols, joinKind, tail string, tailArgs ...any) (string, []any) {
	join, args := f.join(joinKind)
	where, whereArgs := f.where()
	args = append(args, whereArgs...)
	args = append(args, tailArgs...)
	return fmt.Sprintf("SELECT %s FROM commits c %s WHERE %s %s", cols, join, where, tail), args
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

type Summary struct {
	Commits    int   `json:"commits"`
	Authors    int   `json:"authors"`
	Insertions int   `json:"insertions"`
	Deletions  int   `json:"deletions"`
	Files      int   `json:"files"`
	First      int64 `json:"first"`
	Last       int64 `json:"last"`
}

func (s *Store) Summary(ctx context.Context, f Filter) (Summary, error) {
	q, args := f.sql(`COUNT(DISTINCT c.hash), COUNT(DISTINCT c.author_email),
		COALESCE(SUM(fc.insertions), 0), COALESCE(SUM(fc.deletions), 0), COUNT(DISTINCT fc.path),
		COALESCE(MIN(c.author_time), 0), COALESCE(MAX(c.author_time), 0)`, "LEFT", "")
	var r Summary
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&r.Commits, &r.Authors, &r.Insertions,
		&r.Deletions, &r.Files, &r.First, &r.Last)
	return r, err
}

type Bucket struct {
	Start      string `json:"start"` // YYYY-MM-DD, local time
	Commits    int    `json:"commits"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	Authors    int    `json:"authors"`
}

var bucketExpr = map[string]string{
	"day":   `date(c.author_time, 'unixepoch', 'localtime')`,
	"week":  `date(c.author_time, 'unixepoch', 'localtime', '-6 days', 'weekday 1')`,
	"month": `strftime('%Y-%m-01', c.author_time, 'unixepoch', 'localtime')`,
}

// Timeline groups activity by day, week (starting Monday) or month, with
// empty buckets filled in so the series is continuous.
func (s *Store) Timeline(ctx context.Context, f Filter, bucket string) ([]Bucket, error) {
	expr, ok := bucketExpr[bucket]
	if !ok {
		return nil, fmt.Errorf("unknown bucket %q", bucket)
	}
	q, args := f.sql(expr+` AS b, COUNT(DISTINCT c.hash), COALESCE(SUM(fc.insertions), 0),
		COALESCE(SUM(fc.deletions), 0), COUNT(DISTINCT c.author_email)`, "LEFT", "GROUP BY b ORDER BY b")
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var got []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Start, &b.Commits, &b.Insertions, &b.Deletions, &b.Authors); err != nil {
			return nil, err
		}
		got = append(got, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return fillGaps(got, bucket), nil
}

func fillGaps(got []Bucket, bucket string) []Bucket {
	if len(got) == 0 {
		return []Bucket{}
	}
	step := func(t time.Time) time.Time {
		switch bucket {
		case "day":
			return t.AddDate(0, 0, 1)
		case "week":
			return t.AddDate(0, 0, 7)
		default:
			return t.AddDate(0, 1, 0)
		}
	}
	byStart := make(map[string]Bucket, len(got))
	for _, b := range got {
		byStart[b.Start] = b
	}
	first, err1 := time.ParseInLocation(time.DateOnly, got[0].Start, time.Local)
	last, err2 := time.ParseInLocation(time.DateOnly, got[len(got)-1].Start, time.Local)
	if err1 != nil || err2 != nil {
		return got
	}
	var out []Bucket
	for t := first; !t.After(last); t = step(t) {
		key := t.Format(time.DateOnly)
		if b, ok := byStart[key]; ok {
			out = append(out, b)
		} else {
			out = append(out, Bucket{Start: key})
		}
	}
	return out
}

type CommitRow struct {
	Hash        string `json:"hash"`
	Subject     string `json:"subject"`
	AuthorName  string `json:"authorName"`
	AuthorEmail string `json:"authorEmail"`
	AuthorTime  int64  `json:"authorTime"`
	Merge       bool   `json:"merge"`
	Insertions  int    `json:"insertions"`
	Deletions   int    `json:"deletions"`
	Files       int    `json:"files"`
}

func (s *Store) Commits(ctx context.Context, f Filter, limit, offset int) ([]CommitRow, error) {
	q, args := f.sql(`c.hash, c.subject, c.author_name, c.author_email, c.author_time, c.is_merge,
		COALESCE(SUM(fc.insertions), 0), COALESCE(SUM(fc.deletions), 0), COUNT(fc.path)`, "LEFT",
		"GROUP BY c.hash ORDER BY c.author_time DESC, c.hash LIMIT ? OFFSET ?", limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommitRow{}
	for rows.Next() {
		var r CommitRow
		if err := rows.Scan(&r.Hash, &r.Subject, &r.AuthorName, &r.AuthorEmail, &r.AuthorTime,
			&r.Merge, &r.Insertions, &r.Deletions, &r.Files); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type AuthorRow struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	Commits    int    `json:"commits"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	Files      int    `json:"files"`
	First      int64  `json:"first"`
	Last       int64  `json:"last"`
}

func (s *Store) Authors(ctx context.Context, f Filter) ([]AuthorRow, error) {
	q, args := f.sql(`c.author_email, MAX(c.author_name), COUNT(DISTINCT c.hash),
		COALESCE(SUM(fc.insertions), 0), COALESCE(SUM(fc.deletions), 0), COUNT(DISTINCT fc.path),
		MIN(c.author_time), MAX(c.author_time)`, "LEFT",
		"GROUP BY c.author_email ORDER BY 3 DESC, 2")
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuthorRow{}
	for rows.Next() {
		var r AuthorRow
		if err := rows.Scan(&r.Email, &r.Name, &r.Commits, &r.Insertions, &r.Deletions, &r.Files,
			&r.First, &r.Last); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type FileRow struct {
	Path    string `json:"path"`
	Changes int    `json:"changes"` // commits that touched the file
	Churn   int    `json:"churn"`   // insertions + deletions
	Authors int    `json:"authors"`
	Last    int64  `json:"last"`
	Exists  bool   `json:"exists"` // still present at HEAD
}

// Files ranks files by how often they change: a first cut at hotspots.
func (s *Store) Files(ctx context.Context, f Filter, limit int) ([]FileRow, error) {
	q, args := f.sql(`fc.path, COUNT(DISTINCT c.hash), SUM(fc.insertions + fc.deletions),
		COUNT(DISTINCT c.author_email), MAX(c.author_time)`, "",
		"GROUP BY fc.path ORDER BY 2 DESC, 3 DESC LIMIT ?", limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileRow{}
	for rows.Next() {
		var r FileRow
		if err := rows.Scan(&r.Path, &r.Changes, &r.Churn, &r.Authors, &r.Last); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type FileChange struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	Binary     bool   `json:"binary"`
}

type CommitDetail struct {
	CommitRow
	Parents    []string     `json:"parents"`
	CommitTime int64        `json:"commitTime"`
	Changes    []FileChange `json:"changes"`
}

// Commit returns one commit by full or abbreviated hash.
func (s *Store) Commit(ctx context.Context, hash string) (CommitDetail, error) {
	var d CommitDetail
	var parents string
	err := s.db.QueryRowContext(ctx, `SELECT hash, subject, author_name, author_email, author_time,
		is_merge, parents, commit_time FROM commits WHERE hash >= ? AND hash < ? || 'g' ORDER BY hash LIMIT 1`,
		hash, hash).Scan(&d.Hash, &d.Subject, &d.AuthorName, &d.AuthorEmail, &d.AuthorTime, &d.Merge,
		&parents, &d.CommitTime)
	if err != nil {
		return d, err
	}
	d.Parents = strings.Fields(parents)
	rows, err := s.db.QueryContext(ctx, `SELECT path, insertions, deletions, binary FROM file_changes
		WHERE hash = ? ORDER BY path`, d.Hash)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	d.Changes = []FileChange{}
	for rows.Next() {
		var fc FileChange
		if err := rows.Scan(&fc.Path, &fc.Insertions, &fc.Deletions, &fc.Binary); err != nil {
			return d, err
		}
		d.Insertions += fc.Insertions
		d.Deletions += fc.Deletions
		d.Changes = append(d.Changes, fc)
	}
	d.Files = len(d.Changes)
	return d, rows.Err()
}
