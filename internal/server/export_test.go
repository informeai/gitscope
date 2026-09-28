package server

import "testing"

func TestCellNeutralizesFormulas(t *testing.T) {
	cases := map[string]string{
		"feat: login":         "feat: login",
		"=HYPERLINK(\"x\")":   "'=HYPERLINK(\"x\")",
		"+1 fix":              "'+1 fix",
		"-rm":                 "'-rm",
		"@SUM(A1)":            "'@SUM(A1)",
		"":                    "",
		"internal/finance.go": "internal/finance.go",
	}
	for in, want := range cases {
		if got := cell(in); got != want {
			t.Errorf("cell(%q) = %q, want %q", in, got, want)
		}
	}
}
