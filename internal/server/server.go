// Package server exposes the index as a JSON API and serves the web UI.
// It only listens on loopback and rejects requests for other hosts.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gitscope/internal/gitlog"
	"gitscope/internal/store"
)

const maxPatch = 400 * 1024

type Server struct {
	store *store.Store
	root  string
	mux   *http.ServeMux
}

func New(st *store.Store, root string, web fs.FS) *Server {
	s := &Server{store: st, root: root, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/repo", s.repo)
	s.mux.HandleFunc("GET /api/refs", s.refs)
	s.mux.HandleFunc("GET /api/summary", s.summary)
	s.mux.HandleFunc("GET /api/timeline", s.timeline)
	s.mux.HandleFunc("GET /api/commits", s.commits)
	s.mux.HandleFunc("GET /api/commits/{hash}", s.commit)
	s.mux.HandleFunc("GET /api/authors", s.authors)
	s.mux.HandleFunc("GET /api/files", s.files)
	s.mux.HandleFunc("POST /api/sync", s.sync)
	s.mux.Handle("GET /", http.FileServerFS(web))
	return s
}

// ServeHTTP rejects any Host other than loopback, which blocks DNS-rebinding
// attacks from pages open in the same browser.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var bad badRequest
	switch {
	case errors.As(err, &bad):
		status = http.StatusBadRequest
	case errors.Is(err, sql.ErrNoRows):
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}

type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

// filter reads the shared filter parameters:
// author (repeatable), ref, since, until, path, q, merges=1.
func (s *Server) filter(r *http.Request) (store.Filter, error) {
	q := r.URL.Query()
	f := store.Filter{
		Authors: q["author"],
		Path:    strings.TrimPrefix(q.Get("path"), "/"),
		Query:   q.Get("q"),
		Merges:  q.Get("merges") == "1",
	}
	var err error
	if v := q.Get("since"); v != "" {
		if f.Since, err = strconv.ParseInt(v, 10, 64); err != nil {
			return f, badRequest{"invalid since"}
		}
	}
	if v := q.Get("until"); v != "" {
		if f.Until, err = strconv.ParseInt(v, 10, 64); err != nil {
			return f, badRequest{"invalid until"}
		}
	}
	if name := q.Get("ref"); name != "" {
		tip, err := s.resolveRef(r.Context(), name)
		if err != nil {
			return f, err
		}
		if err := s.store.EnsureTip(r.Context(), tip); err != nil {
			return f, err
		}
		f.Tip = tip
	}
	return f, nil
}

// resolveRef only accepts names of existing branches, so user input never
// reaches git as an arbitrary revision.
func (s *Server) resolveRef(ctx context.Context, name string) (string, error) {
	refs, err := gitlog.Refs(ctx, s.root)
	if err != nil {
		return "", err
	}
	for _, r := range refs {
		if r.Name == name {
			return r.Tip, nil
		}
	}
	return "", badRequest{"unknown ref " + name}
}

func intParam(r *http.Request, name string, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n < 0 {
		return def
	}
	return min(n, max)
}

func (s *Server) repo(w http.ResponseWriter, r *http.Request) {
	sum, err := s.store.Summary(r.Context(), store.Filter{Merges: true})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"name":     filepath.Base(s.root),
		"path":     s.root,
		"head":     gitlog.Head(r.Context(), s.root),
		"commits":  sum.Commits,
		"first":    sum.First,
		"last":     sum.Last,
		"syncedAt": s.store.SyncedAt(r.Context()),
	})
}

func (s *Server) refs(w http.ResponseWriter, r *http.Request) {
	refs, err := gitlog.Refs(r.Context(), s.root)
	if err != nil {
		fail(w, err)
		return
	}
	if refs == nil {
		refs = []gitlog.Ref{}
	}
	writeJSON(w, refs)
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		fail(w, err)
		return
	}
	sum, err := s.store.Summary(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, sum)
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		fail(w, err)
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "week"
	}
	if bucket != "day" && bucket != "week" && bucket != "month" {
		fail(w, badRequest{"bucket must be day, week or month"})
		return
	}
	buckets, err := s.store.Timeline(r.Context(), f, bucket)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"bucket": bucket, "buckets": buckets})
}

func (s *Server) commits(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		fail(w, err)
		return
	}
	rows, err := s.store.Commits(r.Context(), f, intParam(r, "limit", 100, 500), intParam(r, "offset", 0, 1<<30))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, rows)
}

var hashRe = regexp.MustCompile(`^[0-9a-f]{4,40}$`)

func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !hashRe.MatchString(hash) {
		fail(w, badRequest{"invalid hash"})
		return
	}
	d, err := s.store.Commit(r.Context(), hash)
	if err != nil {
		fail(w, err)
		return
	}
	patch, truncated, err := gitlog.Show(r.Context(), s.root, d.Hash, maxPatch)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"commit": d, "patch": patch, "truncated": truncated})
}

func (s *Server) authors(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		fail(w, err)
		return
	}
	rows, err := s.store.Authors(r.Context(), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	f, err := s.filter(r)
	if err != nil {
		fail(w, err)
		return
	}
	rows, err := s.store.Files(r.Context(), f, intParam(r, "limit", 100, 1000))
	if err != nil {
		fail(w, err)
		return
	}
	tracked, _ := gitlog.TrackedFiles(r.Context(), s.root) // empty repo: nothing tracked
	for i := range rows {
		rows[i].Exists = tracked[rows[i].Path]
	}
	writeJSON(w, rows)
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.Sync(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, res)
}
