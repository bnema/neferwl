package wayland

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
)

func TestToplevelStates(t *testing.T) {
	tiled := []xdgshell.ToplevelState{xdgshell.ToplevelStateTiledLeft, xdgshell.ToplevelStateTiledRight, xdgshell.ToplevelStateTiledTop, xdgshell.ToplevelStateTiledBottom}
	tests := []struct {
		name    string
		c       ports.ConfigureWindow
		version int32
		want    []xdgshell.ToplevelState
	}{
		{"tiled", ports.ConfigureWindow{Activated: true, Visible: true}, 2, append([]xdgshell.ToplevelState{xdgshell.ToplevelStateActivated, xdgshell.ToplevelStateMaximized}, tiled...)},
		{"tiled v1", ports.ConfigureWindow{Visible: true}, 1, []xdgshell.ToplevelState{xdgshell.ToplevelStateMaximized}},
		{"floating", ports.ConfigureWindow{Floating: true, Activated: true, Visible: true}, 6, []xdgshell.ToplevelState{xdgshell.ToplevelStateActivated}},
		{"fullscreen", ports.ConfigureWindow{Fullscreen: true, Visible: true}, 6, []xdgshell.ToplevelState{xdgshell.ToplevelStateFullscreen}},
		{"hidden tiled v6", ports.ConfigureWindow{}, 6, append([]xdgshell.ToplevelState{xdgshell.ToplevelStateSuspended, xdgshell.ToplevelStateMaximized}, tiled...)},
		{"hidden fullscreen v6", ports.ConfigureWindow{Fullscreen: true}, 6, []xdgshell.ToplevelState{xdgshell.ToplevelStateFullscreen, xdgshell.ToplevelStateSuspended}},
		{"captured hidden tiled v6", ports.ConfigureWindow{Captured: true}, 6, append([]xdgshell.ToplevelState{xdgshell.ToplevelStateMaximized}, tiled...)},
		{"hidden floating v5", ports.ConfigureWindow{Floating: true}, 5, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := toplevelStates(tt.c, tt.version)
			var got []xdgshell.ToplevelState
			for i := 0; i+4 <= len(raw); i += 4 {
				got = append(got, xdgshell.ToplevelState(binary.LittleEndian.Uint32(raw[i:])))
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("states = %v, want %v", got, tt.want)
			}
		})
	}
}
