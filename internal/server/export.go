package server

import (
	"encoding/csv"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitscope/internal/gitlog"
	"gitscope/internal/report"
	"gitscope/internal/store"
)

const (
	maxExportRows    = 1_000_000
	reportAuthorRows = 100
	reportFileRows   = 50
	reportCommitRows = 200
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// attachment makes the browser download the response as
// gitscope-<repo>-<what>-<yyyymmdd>.<ext>.
func (s *Server) attachment(w http.ResponseWriter, what, ext string) {
	repo := unsafeName.ReplaceAllString(filepath.Base(s.root), "-")
	name := fmt.Sprintf("gitscope-%s-%s-%s.%s", repo, what, time.Now().Format("20060102"), ext)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
}

// exportReport renders the current filters as a standalone HTML page.
func (s *Server) exportReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := s.filter(r)
	if err != nil {
		fail(w, err)
		return
	}
	bucket, err := bucketParam(r)
	if err != nil {
		fail(w, err)
		return
	}
	d := report.Data{
		Repo:      filepath.Base(s.root),
		Head:      gitlog.Head(ctx, s.root),
		Generated: time.Now(),
		Filters:   s.describeFilters(r, f),
		Bucket:    bucket,
	}
	if d.Summary, err = s.store.Summary(ctx, f); err != nil {
		fail(w, err)
		return
	}
	if d.Timeline, err = s.store.Timeline(ctx, f, bucket); err != nil {
		fail(w, err)
		return
	}
	if d.Authors, err = s.store.Authors(ctx, f); err != nil {
		fail(w, err)
		return
	}
	d.AuthorsTotal = len(d.Authors)
	d.Authors = d.Authors[:min(len(d.Authors), reportAuthorRows)]
	if d.Files, err = s.store.Files(ctx, f, reportFileRows); err != nil {
		fail(w, err)
		return
	}
	s.markExisting(ctx, d.Files)
	if d.Commits, err = s.store.Commits(ctx, f, reportCommitRows, 0); err != nil {
		fail(w, err)
		return
	}
	css, err := s.reportCSS()
	if err != nil {
		fail(w, err)
		return
	}
	d.CSS = css

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.attachment(w, "relatorio", "html")
	if err := report.Render(w, d); err != nil {
		fail(w, err)
	}
}

// reportCSS inlines the UI stylesheet plus the report and print rules.
func (s *Server) reportCSS() (template.CSS, error) {
	var b strings.Builder
	for _, name := range []string{"style.css", "report.css"} {
		css, err := fs.ReadFile(s.web, name)
		if err != nil {
			return "", err
		}
		b.Write(css)
		b.WriteByte('\n')
	}
	return template.CSS(b.String()), nil // our own embedded files
}

// describeFilters turns the active filters into sentences for the report.
func (s *Server) describeFilters(r *http.Request, f store.Filter) []string {
	var out []string
	if ref := r.URL.Query().Get("ref"); ref != "" {
		out = append(out, "Branch: "+ref)
	}
	if len(f.Authors) > 0 {
		names := map[string]string{}
		if rows, err := s.store.Authors(r.Context(), store.Filter{Authors: f.Authors, Merges: true}); err == nil {
			for _, a := range rows {
				names[a.Email] = a.Name
			}
		}
		var list []string
		for _, email := range f.Authors {
			if name, ok := names[strings.ToLower(email)]; ok {
				list = append(list, fmt.Sprintf("%s <%s>", name, email))
			} else {
				list = append(list, email)
			}
		}
		out = append(out, "Autores: "+strings.Join(list, ", "))
	}
	switch {
	case f.Since > 0 && f.Until > 0:
		out = append(out, fmt.Sprintf("Período: %s a %s", report.Date(f.Since), report.Date(f.Until-86400)))
	case f.Since > 0:
		out = append(out, "Período: a partir de "+report.Date(f.Since))
	case f.Until > 0:
		out = append(out, "Período: até "+report.Date(f.Until-86400))
	}
	if f.Path != "" {
		out = append(out, "Caminho: "+f.Path)
	}
	if f.Query != "" {
		out = append(out, fmt.Sprintf("Mensagem contém: %q", f.Query))
	}
	if f.Merges {
		out = append(out, "Inclui commits de merge")
	}
	return out
}

// exportCSV serves /api/export/{commits,authors,files}.csv with every row
// matching the filters. It uses ';' and a UTF-8 BOM so Excel in pt-BR opens
// it in columns with accents intact.
func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tab, ok := strings.CutSuffix(r.PathValue("file"), ".csv")
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := s.filter(r)
	if err != nil {
		fail(w, err)
		return
	}

	var header []string
	var rows [][]string
	switch tab {
	case "commits":
		list, err := s.store.Commits(ctx, f, maxExportRows, 0)
		if err != nil {
			fail(w, err)
			return
		}
		header = []string{"hash", "data", "autor", "email", "mensagem", "linhas_adicionadas", "linhas_removidas", "arquivos", "merge"}
		for _, c := range list {
			rows = append(rows, []string{c.Hash, isoTime(c.AuthorTime), cell(c.AuthorName), cell(c.AuthorEmail),
				cell(c.Subject), itoa(c.Insertions), itoa(c.Deletions), itoa(c.Files), yesNo(c.Merge)})
		}
	case "authors":
		list, err := s.store.Authors(ctx, f)
		if err != nil {
			fail(w, err)
			return
		}
		header = []string{"autor", "email", "commits", "linhas_adicionadas", "linhas_removidas", "arquivos", "primeiro_commit", "ultimo_commit"}
		for _, a := range list {
			rows = append(rows, []string{cell(a.Name), cell(a.Email), itoa(a.Commits), itoa(a.Insertions),
				itoa(a.Deletions), itoa(a.Files), isoTime(a.First), isoTime(a.Last)})
		}
	case "files":
		list, err := s.store.Files(ctx, f, maxExportRows)
		if err != nil {
			fail(w, err)
			return
		}
		s.markExisting(ctx, list)
		header = []string{"arquivo", "commits", "churn", "autores", "ultima_alteracao", "existe_no_head"}
		for _, fr := range list {
			rows = append(rows, []string{cell(fr.Path), itoa(fr.Changes), itoa(fr.Churn), itoa(fr.Authors),
				isoTime(fr.Last), yesNo(fr.Exists)})
		}
	default:
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	s.attachment(w, tab, "csv")
	_, _ = w.Write(utf8BOM)
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(header)
	_ = cw.WriteAll(rows) // WriteAll flushes
}

// cell neutralizes spreadsheet formulas in text that comes from the
// repository (commit messages, names, paths): a leading = + - @ tab or CR
// would otherwise be evaluated by Excel or Sheets.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func itoa(n int) string { return strconv.Itoa(n) }

func isoTime(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).Format(time.RFC3339)
}

func yesNo(b bool) string {
	if b {
		return "sim"
	}
	return "não"
}
