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
		{"tiled", ports.ConfigureWindow{Activated: true}, 2, append([]xdgshell.ToplevelState{xdgshell.ToplevelStateActivated, xdgshell.ToplevelStateMaximized}, tiled...)},
		{"tiled v1", ports.ConfigureWindow{}, 1, []xdgshell.ToplevelState{xdgshell.ToplevelStateMaximized}},
		{"floating", ports.ConfigureWindow{Floating: true, Activated: true}, 6, []xdgshell.ToplevelState{xdgshell.ToplevelStateActivated}},
		{"fullscreen", ports.ConfigureWindow{Fullscreen: true}, 6, []xdgshell.ToplevelState{xdgshell.ToplevelStateFullscreen}},
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
