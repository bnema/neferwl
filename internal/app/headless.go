package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/neferwl/internal/adapters/headless"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// runHeadless drives one virtual output per size, named HEADLESS-1, -2, ...
// With several outputs, screenshots go to a subdirectory per output.
func runHeadless(ctx context.Context, sizes [][2]int, shots string, hdr bool, ch outputChannels, curs *cursors, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	set := newOutputSet(ctx, ch.captured)
	inventory := ports.OutputHeads{}
	for i, size := range sizes {
		name := fmt.Sprintf("HEADLESS-%d", i+1)
		mode := ports.OutputMode{Width: size[0], Height: size[1], RefreshMilli: 60000, Preferred: true}
		inventory.Heads = append(inventory.Heads, ports.OutputHead{Info: ports.OutputInfo{Name: name, Width: size[0], Height: size[1], RefreshMilli: 60000}, Modes: []ports.OutputMode{mode}, Current: &mode, Enabled: true})
		dir := shots
		if dir != "" && len(sizes) > 1 {
			dir = filepath.Join(shots, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				// Outputs already started stop and release their captures.
				return joinErr(err, set.wait())
			}
		}
		cur := &headless.Cursor{}
		curs.set(name, cur)
		opts := headless.Options{Cursor: cur, LoadCursor: loadCursor, Width: size[0], Height: size[1], ScreenshotDir: dir, HDR: hdr, Formats: ch.formats, Log: log, NewRenderer: newRenderer, Name: name, Presented: ch.presented, Captured: ch.captured}
		set.start(ctx, name, func(octx context.Context, sc <-chan ports.Scene, cc <-chan ports.SurfaceContent, cu <-chan ports.CursorChange, cap <-chan ports.CaptureRequest) error {
			return headless.Run(octx, opts, sc, cc, cu, cap)
		})
		select {
		case ch.events <- ports.OutputAdded{Info: ports.OutputInfo{Name: name, Width: size[0], Height: size[1]}}:
		case <-ctx.Done():
			return set.wait()
		}
	}
	if !ch.sendInventory(ctx, inventory) {
		return set.wait()
	}
	// The pointer starts centred on the first output, like libinput's.
	curs.move("HEADLESS-1", float64(sizes[0][0])/2, float64(sizes[0][1])/2)
	for {
		select {
		case <-ctx.Done():
			return set.wait()
		case s := <-ch.scenes:
			set.scenes(s)
		case c := <-ch.contents:
			set.content(c)
		case q := <-ch.captures:
			set.routeCapture(q)
		case c := <-ch.cursorChanges:
			set.setCursor(c)
		case name := <-set.stopped:
			// A headless output only stops on error: the run ends.
			err := set.finish(name)
			return joinErr(err, set.wait())
		}
	}
}

func joinErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
