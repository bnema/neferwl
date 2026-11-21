package config

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

const resizeMode = `bind.cmd+r = mode resize
mode.resize.bind.h = set-column-width -1
mode.resize.bind.l = set-column-width +1
mode.resize.bind.escape = mode default
`

// hasWarning reports whether one warning on line contains msg.
func hasWarning(w []Warning, line int, msg string) bool {
	for _, x := range w {
		if x.Line == line && strings.Contains(x.Msg, msg) {
			return true
		}
	}
	return false
}

func TestModeParsed(t *testing.T) {
	c, w := parseString(t, resizeMode)
	if len(w) != 0 {
		t.Fatalf("warnings: %v", w)
	}
	if c.Binds["Cmd+r"] != "mode resize" {
		t.Fatalf("enter bind: %q", c.Binds["Cmd+r"])
	}
	got := c.Modes["resize"]
	if len(got) != 3 || got["h"] != "set-column-width -1" || got["l"] != "set-column-width +1" || got["Escape"] != "mode default" {
		t.Fatalf("mode binds: %v", c.Modes)
	}
}

func TestModeDefaultRejected(t *testing.T) {
	c, w := parseString(t, "mode.default.bind.h = focus-column-left")
	if !hasWarning(w, 1, "use bind.h") || c.Modes != nil {
		t.Fatalf("modes %v, warnings %v", c.Modes, w)
	}
}

func TestModeBadName(t *testing.T) {
	c, w := parseString(t, "mode.bad name.bind.h = focus-column-left")
	if !hasWarning(w, 1, "mode name") || c.Modes != nil {
		t.Fatalf("modes %v, warnings %v", c.Modes, w)
	}
	_, w = parseString(t, "mode.resize.h = focus-column-left")
	if !hasWarning(w, 1, "mode.<name>.bind.<keys>") {
		t.Fatalf("missing .bind.: %v", w)
	}
}

func TestModeUndeclaredDropped(t *testing.T) {
	c, w := parseString(t, "bind.cmd+r = focus-column-left; mode ghost")
	if !hasWarning(w, 1, "bind.cmd+r: no mode.ghost.* declared") {
		t.Fatalf("warnings: %v", w)
	}
	if c.Binds["Cmd+r"] != "cycle-column-width" {
		t.Fatalf("default not restored: %q", c.Binds["Cmd+r"])
	}
}

func TestModeNotEnteredWarns(t *testing.T) {
	c, w := parseString(t, "\nmode.resize.bind.h = set-column-width -1\nmode.resize.bind.r = mode resize")
	if !hasWarning(w, 2, "mode.resize: no bind enters it (add bind.<keys> = mode resize)") {
		t.Fatalf("warnings: %v", w)
	}
	if c.Modes["resize"]["h"] != "set-column-width -1" {
		t.Fatalf("mode dropped: %v", c.Modes)
	}
}

func TestModeNoneDropped(t *testing.T) {
	c, w := parseString(t, resizeMode+"mode.resize.bind.x = none\n")
	if _, ok := c.Modes["resize"]["x"]; ok || len(w) != 0 {
		t.Fatalf("modes %v, warnings %v", c.Modes, w)
	}
}

func TestModeCmdCollision(t *testing.T) {
	c, w := parseString(t, resizeMode+"mode.resize.bind.cmd+x = set-column-width -1\nmode.resize.bind.super+x = set-column-width +1\n")
	if !hasWarning(w, 5, "mode.resize.bind.Cmd+x: overridden by mode.resize.bind.Super+x") {
		t.Fatalf("warnings: %v", w)
	}
	if _, ok := c.Modes["resize"]["Cmd+x"]; ok || c.Modes["resize"]["Super+x"] != "set-column-width +1" {
		t.Fatalf("mode binds: %v", c.Modes["resize"])
	}
}

func TestModeWorkspaceBindChecked(t *testing.T) {
	_, w := parseString(t, resizeMode+"mode.resize.bind.w = workspace undeclared\n")
	if !hasWarning(w, 5, "mode.resize.bind.w: no workspace.undeclared.* declared") {
		t.Fatalf("warnings: %v", w)
	}
}

func TestModeBindInRaw(t *testing.T) {
	_, a, _, err := parseBytes([]byte(resizeMode))
	if err != nil {
		t.Fatal(err)
	}
	_, b, _, err := parseBytes([]byte(strings.Replace(resizeMode, "-1", "-2", 1)))
	if err != nil {
		t.Fatal(err)
	}
	key := "mode.resize.bind.h"
	if a[key] != "set-column-width -1" || b[key] != "set-column-width -2" {
		t.Fatalf("raw %q vs %q", a[key], b[key])
	}
}

func TestModeEqualGuard(t *testing.T) {
	c, w := parseString(t, resizeMode+"mode.resize.bind.cmd+ = = set-column-width +1\n")
	if !hasWarning(w, 5, "write cmd+equal instead of cmd+=") || len(c.Modes["resize"]) != 3 {
		t.Fatalf("modes %v, warnings %v", c.Modes, w)
	}
}

func TestModeUndeclaredDroppedFromRaw(t *testing.T) {
	_, raw, _, err := parseBytes([]byte("bind.cmd+shift+r = mode ghost\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["bind.Cmd+Shift+r"]; ok {
		t.Fatalf("raw kept the dropped bind: %v", raw)
	}
}

func TestModeAllBindsDropped(t *testing.T) {
	c, w := parseString(t, "bind.cmd+shift+r = mode resize\nmode.resize.bind.x = none\n")
	if !hasWarning(w, 2, "mode.resize: no binds left") || c.Modes != nil {
		t.Fatalf("modes %v, warnings %v", c.Modes, w)
	}
	if !hasWarning(w, 1, "bind.cmd+shift+r: no mode.resize.* declared") {
		t.Fatalf("enter bind not dropped: %v", w)
	}
}

func TestModeEnterLostToCollision(t *testing.T) {
	c, w := parseString(t, "bind.cmd+shift+r = mode resize\nbind.super+shift+r = quit\nmode.resize.bind.h = set-column-width -1\n")
	if !hasWarning(w, 1, "bind.Cmd+Shift+r: overridden by bind.Shift+Super+r") {
		t.Fatalf("no collision warning: %v", w)
	}
	if !hasWarning(w, 3, "mode.resize: no bind enters it") {
		t.Fatalf("warnings: %v", w)
	}
	if _, ok := c.Modes["resize"]; !ok {
		t.Fatalf("mode dropped: %v", c.Modes)
	}
}

func TestBindWarningsSortedByLine(t *testing.T) {
	_, w := parseString(t, "keyboard.cmd = ctrl\nbind.ctrl+x = quit\nbind.cmd+x = close-window\nbind.cmd+shift+r = mode ghost\nbind.bogus+x = quit\n")
	if len(w) != 3 {
		t.Fatalf("warnings: %v", w)
	}
	for i := 1; i < len(w); i++ {
		if w[i-1].Line > w[i].Line {
			t.Fatalf("unsorted: %v", w)
		}
	}
}

// A mode whose only bind enters an undeclared mode is emptied, so the bind
// entering it is dropped as well.
func TestModeEmptiedByUndeclaredMode(t *testing.T) {
	c, w := parseString(t, "bind.cmd+r = mode a\nmode.a.bind.x = mode ghost\n")
	if !hasWarning(w, 2, "mode.a.bind.x: no mode.ghost.* declared") || !hasWarning(w, 2, "mode.a: no binds left") || !hasWarning(w, 1, "bind.cmd+r: no mode.a.* declared") {
		t.Fatalf("warnings: %v", w)
	}
	if c.Modes != nil || c.Binds["Cmd+r"] != "cycle-column-width" {
		t.Fatalf("modes %v, cmd+r %q", c.Modes, c.Binds["Cmd+r"])
	}
}

// A line naming an undeclared mode is rejected like any invalid line: the
// config, raw and warnings match those with an unknown action instead.
func TestUndeclaredModeLikeInvalidLine(t *testing.T) {
	for _, tc := range []struct{ first, second string }{
		{"bind.cmd+r = quit", "bind.cmd+r = %s"},
		{"bind.cmd+r = none", "bind.cmd+r = %s"},
		{"bind.super+r = quit", "bind.cmd+r = %s"},
		{"bind.cmd+1 = quit", "bind.cmd+code:2 = %s"},
	} {
		t.Run(tc.first, func(t *testing.T) {
			parse := func(action string) (domain.Config, map[string]string, []Warning) {
				c, raw, w, err := parseBytes([]byte(tc.first + "\n" + fmt.Sprintf(tc.second, action) + "\n"))
				if err != nil {
					t.Fatal(err)
				}
				return c, raw, w
			}
			c, raw, w := parse("mode ghost")
			wantC, wantRaw, wantW := parse("bogus")
			if !reflect.DeepEqual(c, wantC) || !maps.Equal(raw, wantRaw) {
				t.Fatalf("binds %v raw %v, want binds %v raw %v", c.Binds, raw, wantC.Binds, wantRaw)
			}
			if len(w) != len(wantW) || !hasWarning(w, 2, "no mode.ghost.* declared") {
				t.Fatalf("warnings %v, want like %v", w, wantW)
			}
			for _, x := range w {
				if strings.Contains(x.Msg, "overrides line 1") {
					t.Fatalf("warnings: %v", w)
				}
			}
		})
	}
}
