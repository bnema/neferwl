package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"strconv"
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

// TestCommands checks the exit code and where each command writes: data
// and help on stdout, an error exactly once on stderr.
func TestCommands(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config"
	badCfg := dir + "/bad"
	stateFile := dir + "/state.json"
	pid := strconv.Itoa(os.Getpid())
	for path, data := range map[string]string{
		cfg:       "",
		badCfg:    "unknown.key = 1\n",
		stateFile: `{"output":"DP-1","outputs":[{"name":"DP-1","active":1,"count":1}],"windows":[{"id":1,"pid":` + pid + `,"output":"DP-1"}]}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NEFERWL_STATE", stateFile)
	for _, tc := range []struct {
		args           []string
		code           int
		stdout, stderr string // substrings; "" means empty
	}{
		{[]string{"--help"}, 0, "neferwl state [output-of <pid>]", ""},
		{[]string{"state", "-h"}, 0, "usage: neferwl state [output-of <pid>]", ""},
		{[]string{"version"}, 0, "1.2.3\n", ""},
		{[]string{"validate-config", cfg}, 0, "ok: " + cfg + "\n", ""},
		{[]string{"validate-config", badCfg}, 1, "", "line 1"},
		{[]string{"state", "output-of", pid}, 0, `{"hdr":{"tier":"","peak_nits":0,"peak_source":"","headroom":0,"active":false,"sdr_expand":"","expandable":false},"name":"DP-1","active":1,"count":1,"workspace":"","workspace_id":""}` + "\n", ""},
		{[]string{"state"}, 0, `"output":"DP-1"`, ""},
		{[]string{"--unknown"}, 2, "", "flag provided but not defined"},
		{[]string{"--backend=invalid"}, 2, "", "invalid backend"},
		{[]string{"--backend=drm", "--screenshot=" + dir}, 2, "", "flag provided but not defined"},
		{[]string{"--backend=drm", "--headless-hdr"}, 2, "", "--headless-hdr requires"},
		{[]string{"--backend=drm", "--input=-"}, 2, "", "flag provided but not defined"},
		{[]string{"--backend=headless", "--screenshot=" + dir, "--screenshot-raw"}, 2, "", "flag provided but not defined"},
		{[]string{"--backend=headless", "--headless-hdr", "--screenshot-raw"}, 2, "", "flag provided but not defined"},
		{[]string{"--backend=server", "--screenshot-raw"}, 2, "", "flag provided but not defined"},
		{[]string{"--backend=server", "--input=-"}, 2, "", "flag provided but not defined"},
		{[]string{"--backend=server", "extra"}, 2, "", "unexpected arguments"},
		{[]string{"--backend=server", "--debug=nope"}, 2, "", "--debug"},
		{[]string{"--size=1920"}, 2, "", "--size"},
		{[]string{"--timeout=-1s"}, 2, "", "negative timeout"},
		{[]string{"--debug=nope"}, 2, "", "--debug"},
		{[]string{"extra"}, 2, "", "unexpected arguments"},
		{[]string{"--config=/nonexistent/neferwl.conf"}, 1, "", "no such file"},
		{[]string{"validate-config", "/nonexistent/neferwl.conf"}, 1, "", "no such file"},
		{[]string{"validate-config", "a", "b"}, 2, "", "usage: neferwl validate-config [path]"},
		{[]string{"state", "output-of", "x"}, 2, "", "usage: neferwl state [output-of <pid>]"},
		{[]string{"state", "extra"}, 2, "", "unexpected arguments"},
		{[]string{"version", "extra"}, 2, "", "usage: neferwl version"},
	} {
		code, stdout, stderr := execute(t, tc.args...)
		if code != tc.code {
			t.Errorf("%v: exit %d, want %d (%s)", tc.args, code, tc.code, stderr)
		}
		if (tc.stdout == "") != (stdout == "") || !strings.Contains(stdout, tc.stdout) {
			t.Errorf("%v: stdout %q, want %q", tc.args, stdout, tc.stdout)
		}
		switch {
		case tc.stderr == "" && stderr != "":
			t.Errorf("%v: unexpected stderr %q", tc.args, stderr)
		case tc.stderr != "" && strings.Count(stderr, tc.stderr) != 1:
			t.Errorf("%v: stderr %q has %q %d times, want once", tc.args, stderr, tc.stderr, strings.Count(stderr, tc.stderr))
		}
		if code == 2 && strings.Count(stderr, "usage:") != 1 {
			t.Errorf("%v: stderr %q needs one usage line", tc.args, stderr)
		}
	}
}

func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		code  int
		print bool
	}{
		{"canceled", context.Canceled, 0, false},
		{"logged", loggedError{errors.New("boom")}, 1, false},
		{"plain", errors.New("boom"), 1, true},
	} {
		var b bytes.Buffer
		if code := exitCode(&b, tc.err); code != tc.code || (b.Len() > 0) != tc.print {
			t.Errorf("%s: exit %d printed %q", tc.name, code, b.String())
		}
	}
}

// TestServerFlagsAllowed validates without running: a server run would grab
// the machine's input devices.
func TestServerFlagsAllowed(t *testing.T) {
	for _, args := range [][]string{
		{"--backend=server"},
		{"--backend=server", "--config=/tmp/x", "--debug=all", "--pprof=localhost:0", "--session", "--no-xwayland", "--no-terminal"},
	} {
		fs := flag.NewFlagSet("neferwl", flag.ContinueOnError)
		var f runFlags
		f.register(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
		sizes, err := f.validate(fs.Args(), set)
		if err != nil || sizes != nil {
			t.Errorf("%v: sizes %v err %v", args, sizes, err)
		}
	}
}
