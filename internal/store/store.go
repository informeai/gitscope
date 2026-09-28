// Package store keeps an indexed copy of the git history in SQLite so that
// filters and comparisons are plain queries.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"gitscope/internal/gitlog"
)

const schema = `
CREATE TABLE IF NOT EXISTS commits (
	hash          TEXT PRIMARY KEY,
	parents       TEXT NOT NULL,
	author_name   TEXT NOT NULL,
	author_email  TEXT NOT NULL,
	author_time   INTEGER NOT NULL,
	commit_time   INTEGER NOT NULL,
	subject       TEXT NOT NULL,
	is_merge      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS commits_time   ON commits(author_time);
CREATE INDEX IF NOT EXISTS commits_author ON commits(author_email);

CREATE TABLE IF NOT EXISTS file_changes (
	hash       TEXT NOT NULL,
	path       TEXT NOT NULL,
	insertions INTEGER NOT NULL,
	deletions  INTEGER NOT NULL,
	binary     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS file_changes_hash ON file_changes(hash);
CREATE INDEX IF NOT EXISTS file_changes_path ON file_changes(path);

-- commits reachable from a ref tip, filled on demand by EnsureTip
CREATE TABLE IF NOT EXISTS ref_commits (
	tip  TEXT NOT NULL,
	hash TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ref_commits_tip ON ref_commits(tip, hash);
CREATE TABLE IF NOT EXISTS ref_tips (tip TEXT PRIMARY KEY);

CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

type Store struct {
	db   *sql.DB
	root string     // repository work tree
	mu   sync.Mutex // serializes Sync and EnsureTip
}

// Open opens (or creates) the index for the repository at root.
func Open(path, root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db, root: root}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SyncResult reports what a Sync changed.
type SyncResult struct {
	Added    int           `json:"added"`
	Removed  int           `json:"removed"`
	Total    int           `json:"total"`
	Duration time.Duration `json:"duration"`
}

// Sync makes the index match every commit reachable from the repository's refs:
// new commits are read with git log, unreachable ones (rebased, deleted
// branches) are dropped.
func (s *Store) Sync(ctx context.Context) (SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := time.Now()

	all, err := gitlog.RevListAll(ctx, s.root)
	if err != nil {
		return SyncResult{}, err
	}
	reachable := make(map[string]bool, len(all))
	for _, h := range all {
		reachable[h] = true
	}
	known, err := s.hashes(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	var missing, stale []string
	for _, h := range all {
		if !known[h] {
			missing = append(missing, h)
		}
	}
	for h := range known {
		if !reachable[h] {
			stale = append(stale, h)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SyncResult{}, err
	}
	defer tx.Rollback()

	for _, h := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM commits WHERE hash = ?`, h); err != nil {
			return SyncResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM file_changes WHERE hash = ?`, h); err != nil {
			return SyncResult{}, err
		}
	}
	if len(missing) > 0 {
		if err := insertCommits(ctx, tx, s.root, missing); err != nil {
			return SyncResult{}, err
		}
	}
	if err := pruneTips(ctx, tx, s.root); err != nil {
		return SyncResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key, value) VALUES ('synced_at', ?)`,
		time.Now().Format(time.RFC3339)); err != nil {
		return SyncResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Added: len(missing), Removed: len(stale), Total: len(all), Duration: time.Since(start)}, nil
}

func (s *Store) hashes(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hash FROM commits`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := make(map[string]bool)
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		known[h] = true
	}
	return known, rows.Err()
}

func insertCommits(ctx context.Context, tx *sql.Tx, root string, hashes []string) error {
	insCommit, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO commits
		(hash, parents, author_name, author_email, author_time, commit_time, subject, is_merge)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insCommit.Close()
	insFile, err := tx.PrepareContext(ctx, `INSERT INTO file_changes
		(hash, path, insertions, deletions, binary) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insFile.Close()

	return gitlog.Log(ctx, root, hashes, func(c gitlog.Commit) error {
		if _, err := insCommit.ExecContext(ctx, c.Hash, strings.Join(c.Parents, " "), c.AuthorName,
			c.AuthorEmail, c.AuthorTime, c.CommitTime, c.Subject, len(c.Parents) > 1); err != nil {
			return err
		}
		for _, f := range c.Files {
			if _, err := insFile.ExecContext(ctx, c.Hash, f.Path, f.Insertions, f.Deletions, f.Binary); err != nil {
				return err
			}
		}
		return nil
	})
}

// pruneTips drops cached reachability for tips no ref points at anymore.
func pruneTips(ctx context.Context, tx *sql.Tx, root string) error {
	refs, err := gitlog.Refs(ctx, root)
	if err != nil {
		return err
	}
	current := make([]any, 0, len(refs))
	for _, r := range refs {
		current = append(current, r.Tip)
	}
	notIn := "1=1"
	if len(current) > 0 {
		notIn = "tip NOT IN (" + placeholders(len(current)) + ")"
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ref_commits WHERE `+notIn, current...); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM ref_tips WHERE `+notIn, current...)
	return err
}

// EnsureTip caches the commits reachable from tip so filters can use it.
func (s *Store) EnsureTip(ctx context.Context, tip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ref_tips WHERE tip = ?`, tip).Scan(&n); err != nil || n > 0 {
		return err
	}
	hashes, err := gitlog.RevList(ctx, s.root, tip)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ins, err := tx.PrepareContext(ctx, `INSERT INTO ref_commits(tip, hash) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, h := range hashes {
		if _, err := ins.ExecContext(ctx, tip, h); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ref_tips(tip) VALUES (?)`, tip); err != nil {
		return err
	}
	return tx.Commit()
}

// SyncedAt returns when the index was last synced.
func (s *Store) SyncedAt(ctx context.Context) string {
	var v string
	_ = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'synced_at'`).Scan(&v)
	return v
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
