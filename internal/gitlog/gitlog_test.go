package gitlog

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	out := "\x1eaaa\x1f\x1fAna\x1fAna@Example.com\x1f100\x1f110\x1ffeat: first\n" +
		"\n" +
		"10\t2\tinternal/finance/interest.go\n" +
		"-\t-\tdocs/logo.png\n" +
		"\x1ebbb\x1faaa ccc\x1fBruno\x1fbruno@example.com\x1f200\x1f210\x1fMerge branch 'x'\n" +
		"\x1eccc\x1faaa\x1fAna\x1fana@example.com\x1f150\x1f150\x1ffix: tab\tin subject\n" +
		"\n" +
		"1\t0\tREADME.md\n"

	var got []Commit
	if err := parse(strings.NewReader(out), func(c Commit) error {
		got = append(got, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := []Commit{
		{Hash: "aaa", Parents: []string{}, AuthorName: "Ana", AuthorEmail: "ana@example.com", AuthorTime: 100, CommitTime: 110,
			Subject: "feat: first", Files: []FileChange{
				{Path: "internal/finance/interest.go", Insertions: 10, Deletions: 2},
				{Path: "docs/logo.png", Binary: true},
			}},
		{Hash: "bbb", Parents: []string{"aaa", "ccc"}, AuthorName: "Bruno", AuthorEmail: "bruno@example.com",
			AuthorTime: 200, CommitTime: 210, Subject: "Merge branch 'x'"},
		{Hash: "ccc", Parents: []string{"aaa"}, AuthorName: "Ana", AuthorEmail: "ana@example.com",
			AuthorTime: 150, CommitTime: 150, Subject: "fix: tab\tin subject", Files: []FileChange{
				{Path: "README.md", Insertions: 1},
			}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse() =\n%+v\nwant\n%+v", got, want)
	}
}
