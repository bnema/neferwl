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
func runHeadless(ctx context.Context, sizes [][2]int, shots string, events chan<- ports.OutputEvent, scenes <-chan []ports.Scene, contents <-chan ports.SurfaceContent, cursorChanges <-chan ports.CursorChange, presented chan<- ports.OutputPresented, captures <-chan ports.CaptureRequest, captured chan<- ports.CaptureDone, curs *cursors, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	set := newOutputSet(captured)
	for i, size := range sizes {
		name := fmt.Sprintf("HEADLESS-%d", i+1)
		dir := shots
		if dir != "" && len(sizes) > 1 {
			dir = filepath.Join(shots, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		cur := &headless.Cursor{}
		curs.set(name, cur)
		opts := headless.Options{Cursor: cur, LoadCursor: loadCursor, Width: size[0], Height: size[1], ScreenshotDir: dir, Log: log, NewRenderer: newRenderer, Name: name, Presented: presented, Captured: captured}
		set.start(ctx, name, func(octx context.Context, sc <-chan ports.Scene, cc <-chan ports.SurfaceContent, cu <-chan ports.CursorChange, cap <-chan ports.CaptureRequest) error {
			return headless.Run(octx, opts, sc, cc, cu, cap)
		})
		select {
		case events <- ports.OutputAdded{Info: ports.OutputInfo{Name: name, Width: size[0], Height: size[1]}}:
		case <-ctx.Done():
			return set.wait()
		}
	}
	// The pointer starts centred on the first output, like libinput's.
	curs.move("HEADLESS-1", float64(sizes[0][0])/2, float64(sizes[0][1])/2)
	for {
		select {
		case <-ctx.Done():
			return set.wait()
		case s := <-scenes:
			set.scenes(s)
		case c := <-contents:
			set.content(c)
		case q := <-captures:
			set.routeCapture(q)
		case c := <-cursorChanges:
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
