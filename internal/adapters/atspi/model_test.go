package atspi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/ports"
)

func TestToTargetConvertsWindowCoordinates(t *testing.T) {
	win := ports.Rect{X: 100, Y: 50, W: 800, H: 600}
	// The frame origin (fx, fy) is subtracted: toolkit offsets cancel out.
	got, ok := toTarget(ports.HintWindow{Rect: win}, 10, 20, element{role: roleLink, x: 110, y: 120, w: 40, h: 16})
	require.True(t, ok)
	require.Equal(t, ports.HintTarget{Rect: ports.Rect{X: 200, Y: 150, W: 40, H: 16}, Kind: ports.HintLink, Source: ports.HintSourceATSPI, Score: 1}, got)
}

func TestToTargetClipsToWindow(t *testing.T) {
	win := ports.Rect{X: 100, Y: 50, W: 200, H: 100}
	got, ok := toTarget(ports.HintWindow{Rect: win}, 0, 0, element{role: rolePushButton, x: 180, y: 90, w: 60, h: 30})
	require.True(t, ok)
	require.Equal(t, ports.Rect{X: 280, Y: 140, W: 20, H: 10}, got.Rect)
	require.Equal(t, ports.HintButton, got.Kind)
}

func TestToTargetDropsEmptyAndOutside(t *testing.T) {
	win := ports.Rect{X: 0, Y: 0, W: 100, H: 100}
	for name, e := range map[string]element{
		"zero width":  {role: roleLink, x: 1, y: 1, w: 0, h: 10},
		"neg height":  {role: roleLink, x: 1, y: 1, w: 10, h: -1},
		"outside":     {role: roleLink, x: 500, y: 1, w: 10, h: 10},
		"above":       {role: roleLink, x: 1, y: -50, w: 10, h: 10},
		"left of win": {role: roleLink, x: -30, y: 1, w: 10, h: 10},
	} {
		_, ok := toTarget(ports.HintWindow{Rect: win}, 0, 0, e)
		require.False(t, ok, name)
	}
}

func TestRoleKindMapping(t *testing.T) {
	require.Equal(t, ports.HintLink, kindOf(roleLink))
	for _, r := range []uint32{rolePushButton, roleToggleButton, roleCheckBox, roleRadioButton, roleMenuItem, rolePageTab, roleComboBox} {
		require.Equal(t, ports.HintButton, kindOf(r), r)
		require.True(t, isClickable(r, false), r)
		require.False(t, isClickable(r, true), r)
	}
	require.True(t, isClickable(roleLink, true))
	require.False(t, isClickable(roleFrame, false))
	require.True(t, isWindowRole(roleFrame) && isWindowRole(roleWindow) && isWindowRole(roleDialog))
	require.False(t, isWindowRole(roleLink))
}

func TestRoleAndStateBits(t *testing.T) {
	require.Equal(t, []int32{0, 0, 0, 0, 0}, roleBits(nil))
	// link is role 88: word 2, bit 24; push button is 43: word 1, bit 11.
	require.Equal(t, []int32{0, 1 << 11, 1 << 24, 0, 0}, roleBits([]uint32{roleLink, rolePushButton}))
	// showing is state 25, visible 30: both in word 0.
	require.Equal(t, []int32{1<<25 | 1<<30, 0}, stateBits(stateShowing, stateVisible))
}

func wins(ws ...ports.HintWindow) []ports.HintWindow { return ws }

func win(id ports.WindowID, w, h int, focused bool) ports.HintWindow {
	return ports.HintWindow{ID: id, PID: 1, Rect: ports.Rect{W: w, H: h}, Focused: focused}
}

func TestMatchFramesSingle(t *testing.T) {
	got := matchFrames(wins(win(1, 800, 600, false)), []frameInfo{{w: 100, h: 100}})
	require.Equal(t, map[int]int{0: 0}, got)
}

func TestMatchFramesActive(t *testing.T) {
	frames := []frameInfo{{w: 800, h: 600}, {w: 800, h: 600, active: true}}
	got := matchFrames(wins(win(1, 800, 600, true), win(2, 800, 600, false)), frames)
	// The focused window takes the ACTIVE frame and reserves it; the other
	// window then only has the remaining frame to match.
	require.Equal(t, map[int]int{0: 1, 1: 0}, got)
}

func TestMatchFramesSize(t *testing.T) {
	frames := []frameInfo{{w: 1000, h: 700}, {w: 400, h: 300}}
	got := matchFrames(wins(win(1, 1000, 700, false), win(2, 420, 290, false)), frames)
	require.Equal(t, map[int]int{0: 0, 1: 1}, got)
}

func TestMatchFramesSizeTolerance(t *testing.T) {
	frames := []frameInfo{{w: 840, h: 640}, {w: 100, h: 100}}
	other := win(2, 90, 90, false)
	// 40 px off still matches; 41 px does not.
	require.Equal(t, map[int]int{0: 0, 1: 1}, matchFrames(wins(win(1, 800, 600, false), other), frames))
	require.Equal(t, map[int]int{1: 1}, matchFrames(wins(win(1, 799, 600, false), other), frames))
}

func TestMatchFramesAmbiguous(t *testing.T) {
	// Two windows and two frames of the same size, none active: skipped.
	frames := []frameInfo{{w: 800, h: 600}, {w: 810, h: 600}}
	got := matchFrames(wins(win(1, 800, 600, false), win(2, 800, 600, false)), frames)
	require.Empty(t, got)
	// One window, two same-size frames, not focused: skipped.
	require.Empty(t, matchFrames(wins(win(1, 800, 600, false)), frames))
	// No frame close enough.
	require.Empty(t, matchFrames(wins(win(1, 200, 200, false), win(2, 300, 300, false)), frames))
}

// The active frame is reserved by the focused window: an unfocused window of
// the same size as that frame must not be paired with it as well.
func TestMatchFramesOneToOne(t *testing.T) {
	frames := []frameInfo{{ref: ref{"a", "/active"}, active: true, w: 400, h: 300}, {ref: ref{"a", "/idle"}, w: 800, h: 600}}
	got := matchFrames(wins(win(1, 800, 600, true), win(2, 400, 300, false)), frames)
	seen := map[int]bool{}
	for _, f := range got {
		require.False(t, seen[f], "frame %d assigned twice: %v", f, got)
		seen[f] = true
	}
	require.Equal(t, map[int]int{0: 0}, got)
}
