package captureallow

import (
	"errors"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		mode     Mode
		allowed  []string
		refused  []string
		errLine  int
		wantFail bool
	}{
		{name: "paths comments blanks", in: "# header\n\n/usr/bin/grim\n  /usr/lib/xdg-desktop-portal-wlr  \n\t# indented comment\n/opt/a#b\n",
			mode: ModeList, allowed: []string{"/usr/bin/grim", "/usr/lib/xdg-desktop-portal-wlr", "/opt/a#b"}, refused: []string{"/usr/bin/grim2", "/usr/bin", "grim", ""}},
		{name: "cleaned", in: "/usr//bin/../bin/grim\n", mode: ModeList, allowed: []string{"/usr/bin/grim"}},
		{name: "no trailing newline", in: "/a/b", mode: ModeList, allowed: []string{"/a/b"}},
		{name: "empty file allows nothing", in: "", mode: ModeList, refused: []string{"/usr/bin/grim"}},
		{name: "star disables", in: "# all\n*\n", mode: ModeDisabled, allowed: []string{"/anything"}},
		{name: "star with paths still disables", in: "/usr/bin/grim\n*\n", mode: ModeDisabled, allowed: []string{"/x"}},
		{name: "relative", in: "/ok\ngrim\n", errLine: 2, wantFail: true},
		{name: "dot relative", in: "./grim\n", errLine: 1, wantFail: true},
		{name: "glob is not star", in: "/usr/bin/*\n", mode: ModeList, allowed: []string{"/usr/bin/*"}, refused: []string{"/usr/bin/grim"}},
		{name: "star inside garbage", in: "# c\n\n* \nfoo bar\n", errLine: 4, wantFail: true},
		{name: "directory", in: "/usr/bin/\n", errLine: 1, wantFail: true},
		{name: "nul", in: "/usr/bin/g\x00rim\n", errLine: 1, wantFail: true},
		{name: "windows newline", in: "/a/b\r\n", mode: ModeList, allowed: []string{"/a/b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(strings.NewReader(tc.in))
			if tc.wantFail {
				var pe *ParseError
				if !errors.As(err, &pe) || pe.Line != tc.errLine || p != nil {
					t.Fatalf("Parse = %v, %v; want ParseError on line %d", p, err, tc.errLine)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Mode() != tc.mode {
				t.Fatalf("mode %v, want %v", p.Mode(), tc.mode)
			}
			for _, e := range tc.allowed {
				if !p.Allows(e) {
					t.Errorf("%q refused", e)
				}
			}
			for _, e := range tc.refused {
				if p.Allows(e) {
					t.Errorf("%q allowed", e)
				}
			}
		})
	}
}

func TestParseTooLarge(t *testing.T) {
	_, err := Parse(strings.NewReader(strings.Repeat("# filler line\n", maxSize/10)))
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ParseError", err)
	}
	_, err = Parse(strings.NewReader("/" + strings.Repeat("a", maxSize+10)))
	if !errors.As(err, &pe) {
		t.Fatalf("long line err = %v, want ParseError", err)
	}
}

func TestBuiltin(t *testing.T) {
	p := Builtin()
	want := []string{"/usr/bin/grim", "/usr/bin/nefercap", "/usr/lib/xdg-desktop-portal-wlr", "/usr/libexec/xdg-desktop-portal-wlr"}
	if p.Mode() != ModeList || p.Len() != len(want) {
		t.Fatalf("mode %v, len %d", p.Mode(), p.Len())
	}
	for _, e := range want {
		if !p.Allows(e) {
			t.Fatalf("%s refused", e)
		}
	}
	if p.Allows("/usr/bin/other") {
		t.Fatal("unlisted allowed")
	}
}

func TestPolicyModes(t *testing.T) {
	if !Disabled().Allows("/x") || Closed().Allows("/x") || (&Policy{}).Allows("/x") {
		t.Fatal("Disabled allows, Closed and the zero value refuse")
	}
	p, _ := Parse(strings.NewReader("/a\n"))
	if p.Allows("a") {
		t.Fatal("relative exe allowed")
	}
}
