package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// execute runs args like Main and returns the exit code and output.
func execute(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := Env{Stdout: &out, Stderr: &errOut, Version: "1.2.3"}
	code = exitCode(&errOut, Execute(context.Background(), env, args))
	return code, out.String(), errOut.String()
}

func TestMergeDebug(t *testing.T) {
	if got := mergeDebug([]string{"core", "app"}, "render,core"); got != "core,app,render,core" {
		t.Fatal(got)
	}
	if got := mergeDebug(nil, ""); got != "" {
		t.Fatal(got)
	}
	if got := mergeDebug([]string{"core"}, ""); got != "core" {
		t.Fatal(got)
	}
	if n := testing.AllocsPerRun(10, func() { _ = mergeDebug([]string{"core", "app"}, "render") }); n > 1 {
		t.Fatalf("%v allocations, want 1", n)
	}
}

func TestParseSizes(t *testing.T) {
	sizes, err := parseSizes("1920x1080, 1280x720")
	if err != nil || len(sizes) != 2 || sizes[1] != [2]int{1280, 720} {
		t.Fatal(sizes, err)
	}
}
func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--help"}, 0},
		{[]string{"state", "-h"}, 0},
		{[]string{"--unknown"}, 2},
		{[]string{"--backend=invalid"}, 2},
		{[]string{"--backend=drm", "--screenshot=/tmp/shots"}, 2},
		{[]string{"--backend=drm", "--headless-hdr"}, 2},
		{[]string{"--backend=drm", "--input=-"}, 2},
		{[]string{"--backend=headless", "--screenshot=/tmp/shots", "--screenshot-raw"}, 2},
		{[]string{"--backend=headless", "--headless-hdr", "--screenshot-raw"}, 2},
		{[]string{"--size=1920"}, 2},
		{[]string{"--timeout=-1s"}, 2},
		{[]string{"extra"}, 2},
		{[]string{"--config=/nonexistent/neferwl.conf"}, 1},
		{[]string{"validate-config", "/nonexistent/neferwl.conf"}, 1},
		{[]string{"validate-config", "a", "b"}, 2},
		{[]string{"state", "output-of", "x"}, 2},
		{[]string{"state", "extra"}, 2},
		{[]string{"version", "extra"}, 2},
	} {
		if got, _, stderr := execute(t, tc.args...); got != tc.code {
			t.Errorf("%v: %d want %d (%s)", tc.args, got, tc.code, stderr)
		}
	}
}

func TestUsageErrorNamesCommand(t *testing.T) {
	_, _, stderr := execute(t, "state", "extra")
	if !strings.Contains(stderr, "usage: neferwl state [output-of <pid>]") {
		t.Fatal(stderr)
	}
}

func TestHelpListsCommandsAndFlags(t *testing.T) {
	_, stdout, _ := execute(t, "--help")
	for _, want := range []string{"validate-config [path]", "state [output-of <pid>]", "version", "-backend"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help lacks %q:\n%s", want, stdout)
		}
	}
}

func TestVersion(t *testing.T) {
	if code, stdout, _ := execute(t, "version"); code != 0 || stdout != "1.2.3\n" {
		t.Fatalf("%d %q", code, stdout)
	}
}

func TestValidateConfig(t *testing.T) {
	path := t.TempDir() + "/config"
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stdout, _ := execute(t, "validate-config", path); code != 0 || stdout != "ok: "+path+"\n" {
		t.Fatalf("%d %q", code, stdout)
	}
}

func TestCanceledIsCleanExit(t *testing.T) {
	if code := exitCode(io.Discard, context.Canceled); code != 0 {
		t.Fatal(code)
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
