package main

import (
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
		{[]string{"nefertty", "--help"}, 0},
		{[]string{"nefertty", "--backend=invalid"}, 2},
		{[]string{"nefertty", "--backend=drm", "--screenshot=/tmp/shots"}, 2},
		{[]string{"nefertty", "--timeout=-1s"}, 2},
		{[]string{"nefertty", "extra"}, 2},
		{[]string{"nefertty", "--config=/nonexistent/nefertty.conf"}, 1},
		{[]string{"nefertty", "validate-config", "/nonexistent/nefertty.conf"}, 1},
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
