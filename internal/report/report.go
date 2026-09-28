// Package report renders a self-contained HTML snapshot of the current view:
// no scripts and no external assets, so it opens anywhere and prints cleanly.
package report

import (
	"embed"
	"fmt"
	"html"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"

	"gitscope/internal/store"
)

//go:embed report.html
var files embed.FS

var tmpl = template.Must(template.New("report.html").Funcs(template.FuncMap{
	"num":      Num,
	"date":     Date,
	"dateTime": func(t time.Time) string { return t.Format("02/01/2006 15:04") },
	"short":    func(h string) string { return h[:min(7, len(h))] },
	"pct": func(v, max int) string {
		if max == 0 {
			return "0"
		}
		return strconv.FormatFloat(float64(v)/float64(max)*100, 'f', 1, 64)
	},
}).ParseFS(files, "report.html"))

// Data is everything the report shows. Lists are already cut to the size
// shown; the *Total fields hold the full counts.
type Data struct {
	Repo         string
	Head         string
	Generated    time.Time
	Filters      []string // human-readable, empty means the whole history
	Summary      store.Summary
	Bucket       string
	Timeline     []store.Bucket
	Authors      []store.AuthorRow
	AuthorsTotal int
	Files        []store.FileRow
	Commits      []store.CommitRow
	CSS          template.CSS
}

// Render writes the report page.
func Render(w io.Writer, d Data) error {
	view := struct {
		Data
		TimelineSVG    template.HTML
		BucketName     string
		MaxAuthorCount int
		MaxFileCount   int
	}{Data: d, TimelineSVG: timelineSVG(d.Timeline, d.Bucket), BucketName: bucketNames[d.Bucket]}
	for _, a := range d.Authors {
		view.MaxAuthorCount = max(view.MaxAuthorCount, a.Commits)
	}
	for _, f := range d.Files {
		view.MaxFileCount = max(view.MaxFileCount, f.Changes)
	}
	return tmpl.Execute(w, view)
}

var bucketNames = map[string]string{"day": "por dia", "week": "por semana", "month": "por mês"}

var months = []string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"}

func bucketLabel(start, bucket string) string {
	t, err := time.ParseInLocation(time.DateOnly, start, time.Local)
	if err != nil {
		return start
	}
	if bucket == "month" {
		return fmt.Sprintf("%s/%02d", months[t.Month()-1], t.Year()%100)
	}
	return fmt.Sprintf("%02d %s", t.Day(), months[t.Month()-1])
}

// timelineSVG draws the commit bars with native tooltips (<title>), so the
// chart stays readable without JavaScript.
func timelineSVG(buckets []store.Bucket, bucket string) template.HTML {
	if len(buckets) == 0 {
		return `<div class="empty">Nenhum commit no filtro atual</div>`
	}
	const W, H = 1000.0, 200.0
	const top, right, bottom, left = 8.0, 4.0, 22.0, 34.0
	iw, ih := W-left-right, H-top-bottom
	maxC := 1
	for _, b := range buckets {
		maxC = max(maxC, b.Commits)
	}
	bw := iw / float64(len(buckets))
	gap := 0.0
	if bw > 6 {
		gap = min(3, bw*0.2)
	}
	y := func(v int) float64 { return top + ih - float64(v)/float64(maxC)*ih }
	labelEvery := max(1, (len(buckets)+13)/14)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="Commits %s">`, W, H, bucketNames[bucket])
	ticks := []int{0, maxC / 2, maxC}
	for i, t := range ticks {
		if i > 0 && t == ticks[i-1] {
			continue
		}
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, left, W-right, y(t), y(t))
		fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.1f" text-anchor="end">%d</text>`, left-6, y(t)+4, t)
	}
	for i, bk := range buckets {
		x := left + float64(i)*bw
		label := html.EscapeString(bucketLabel(bk.Start, bucket))
		if bk.Commits > 0 {
			fmt.Fprintf(&b, `<rect class="bar" x="%.2f" y="%.2f" width="%.2f" height="%.2f"><title>%s · %s commit(s) · +%s −%s · %d autor(es)</title></rect>`,
				x+gap/2, y(bk.Commits), max(1, bw-gap), max(1, top+ih-y(bk.Commits)),
				label, Num(bk.Commits), Num(bk.Insertions), Num(bk.Deletions), bk.Authors)
		}
		if i%labelEvery == 0 {
			fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, x+bw/2, H-6, label)
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// Num formats an integer the pt-BR way: 12.345.
func Num(n int) string {
	s := strconv.Itoa(max(n, -n))
	var b strings.Builder
	if n < 0 {
		b.WriteByte('-')
	}
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Date formats unix seconds as dd/mm/yyyy in local time.
func Date(unix int64) string {
	if unix == 0 {
		return "—"
	}
	return time.Unix(unix, 0).Format("02/01/2006")
}
