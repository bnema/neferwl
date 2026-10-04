package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestMergeDebug(t *testing.T) {
	if got := mergeDebug([]string{"core", "app"}, "render,core"); got != "core,app,render,core" {
		t.Fatal(got)
	}
	if got := mergeDebug(nil, ""); got != "" {
		t.Fatal(got)
	}
}
func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"neferwl", "--help"}, 0},
		{[]string{"neferwl", "--backend=invalid"}, 2},
		{[]string{"neferwl", "--backend=drm", "--screenshot=/tmp/shots"}, 2},
		{[]string{"neferwl", "--timeout=-1s"}, 2},
		{[]string{"neferwl", "extra"}, 2},
		{[]string{"neferwl", "--config=/nonexistent/neferwl.conf"}, 1},
		{[]string{"neferwl", "validate-config", "/nonexistent/neferwl.conf"}, 1},
	} {
		old := os.Args
		os.Args = tc.args
		got := runCode()
		os.Args = old
		if got != tc.code {
			t.Errorf("%v: %d want %d", tc.args, got, tc.code)
		}
	}
	if !strings.Contains(mergeDebug([]string{"core"}, "render"), "render") {
		t.Fatal("union")
	}
}

func TestOpenScript(t *testing.T) {
	file := t.TempDir() + "/script"
	if err := os.WriteFile(file, []byte("quit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		check      func(t *testing.T, script io.ReadCloser, err error)
	}{
		{"none", "", func(t *testing.T, script io.ReadCloser, err error) {
			if err != nil || script != nil {
				t.Fatalf("got %#v, %v; want a nil interface", script, err)
			}
		}},
		{"stdin", "-", func(t *testing.T, script io.ReadCloser, err error) {
			if err != nil || script != io.ReadCloser(os.Stdin) {
				t.Fatalf("got %#v, %v; want os.Stdin", script, err)
			}
		}},
		{"missing", "/nonexistent/script", func(t *testing.T, script io.ReadCloser, err error) {
			if err == nil || script != nil {
				t.Fatalf("got %#v, %v; want a nil interface and an error", script, err)
			}
		}},
		{"file", file, func(t *testing.T, script io.ReadCloser, err error) {
			if err != nil || script == nil {
				t.Fatalf("got %#v, %v", script, err)
			}
			if got, err := io.ReadAll(script); err != nil || string(got) != "quit\n" {
				t.Fatalf("read %q, %v", got, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, closeScript, err := openScript(tc.path)
			if closeScript == nil {
				t.Fatal("nil close func")
			}
			tc.check(t, script, err)
			closeScript()
			if tc.path == "-" {
				if _, err := os.Stdin.Stat(); err != nil {
					t.Fatalf("stdin closed: %v", err)
				}
			}
			if tc.name == "file" {
				if _, err := script.Read(make([]byte, 1)); err == nil || err == io.EOF {
					t.Fatalf("file left open: %v", err)
				}
			}
		})
	}
}
