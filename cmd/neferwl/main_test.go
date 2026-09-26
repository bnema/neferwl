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
