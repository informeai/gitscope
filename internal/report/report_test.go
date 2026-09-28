package report

import (
	"strings"
	"testing"
	"time"

	"gitscope/internal/store"
)

func TestRenderEscapesGitContent(t *testing.T) {
	var b strings.Builder
	err := Render(&b, Data{
		Repo:         "erp",
		Generated:    time.Date(2026, 9, 28, 14, 30, 0, 0, time.Local),
		Filters:      []string{`Mensagem contém: "<b>x</b>"`},
		Summary:      store.Summary{Commits: 12345, First: 1, Last: 2},
		Bucket:       "month",
		Timeline:     []store.Bucket{{Start: "2026-08-01", Commits: 3}, {Start: "2026-09-01"}},
		Authors:      []store.AuthorRow{{Name: "<img src=x onerror=alert(1)>", Email: "a@b", Commits: 3}},
		AuthorsTotal: 1,
		Files:        []store.FileRow{{Path: "gone.go", Changes: 1}},
		Commits:      []store.CommitRow{{Hash: "abcdef1234", Subject: "<script>alert(1)</script>"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, bad := range []string{"<script>alert", "<img src=x", "<b>x</b>"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains unescaped %q", bad)
		}
	}
	for _, want := range []string{"12.345", "ago/26", "abcdef1", "removido", "<svg", "<title>ago/26 · 3 commit(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestNum(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1.000", 1234567: "1.234.567", -4200: "-4.200"} {
		if got := Num(n); got != want {
			t.Errorf("Num(%d) = %q, want %q", n, got, want)
		}
	}
}
