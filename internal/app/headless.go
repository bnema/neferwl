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
// With several outputs, screenshots go to a subdirectory per output. Modes
// are fixed, so apply answers every configuration without backend work.
//
// Virtual outputs have no display to list modifiers, so each renderer is
// told it drives one (vulkan.Renderer.SetVirtualOutput); without it a real
// GPU refuses the HDR targets and the output falls back to SDR.
func runHeadless(ctx context.Context, sizes [][2]int, shots string, hdr bool, apply *outputApply, ch outputChannels, curs *cursors, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	newRenderer = virtualRenderer(newRenderer)
	set := newOutputSet(ctx, ch.captured)
	set.wireSecurity(ch)
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
		opts := headless.Options{Cursor: cur, LoadCursor: loadCursor, Width: size[0], Height: size[1], ScreenshotDir: dir, HDR: hdr, Formats: ch.formats, Log: log, NewRenderer: newRenderer, NewCaptureRenderer: newRenderer, Name: name, Presented: ch.presented, Captured: ch.captured}
		started := set.start(ctx, name, func(octx context.Context, sc <-chan ports.Scene, cc <-chan ports.SurfaceContent, cu <-chan ports.CursorChange, cap <-chan ports.CaptureRequest, secure <-chan ports.SecurityState, instance ports.OutputInstance) error {
			opts.Security, opts.SecurityChanges, opts.Instance = ch.security, secure, instance
			return headless.Run(octx, opts, sc, cc, cu, cap)
		}, &opts.SecurityEvents)
		if !started {
			return set.wait()
		}
		select {
		case ch.events <- ports.OutputAdded{Info: ports.OutputInfo{Name: name, Width: size[0], Height: size[1]}}:
		case <-ctx.Done():
			return set.wait()
		}
	}
	// All virtual lifetimes have registered before this no-lease barrier.
	if set.state.Protected {
		set.securityEvent(ports.SecurityBackendBarrier{Generation: set.state.Generation})
	}
	apply.heads(inventory)
	// The pointer starts centred on the first output, like libinput's.
	curs.move("HEADLESS-1", float64(sizes[0][0])/2, float64(sizes[0][1])/2, false)
	for {
		configs, configNext := apply.configOut(ch.configured), apply.config
		replies, replyNext := apply.replyOut(ch.replies)
		heads, headsNext := apply.headsOut(ch.heads)
		contents, contentOut, contentNext, contentOwner := set.contentOut(ch.contents)
		select {
		case <-ctx.Done():
			return set.wait()
		case state, ok := <-ch.securityChanges:
			if !ok {
				ch.securityChanges = nil
				continue
			}
			if set.setSecurity(state) && set.state.Protected {
				set.securityEvent(ports.SecurityBackendBarrier{Generation: set.state.Generation})
			}
		case ev := <-ch.reloads:
			apply.reload(ev.Config)
		case req := <-ch.requests:
			apply.request(req)
		case configs <- configNext:
			apply.configSent()
		case replies <- replyNext:
			apply.replySent()
		case heads <- headsNext:
			apply.headsSent()
		case s := <-ch.scenes:
			set.scenes(s)
		case c := <-contents:
			set.content(c)
		case contentOut <- contentNext:
			contentOwner.contentSent()
		case q := <-ch.captures:
			set.routeCapture(q)
		case c := <-ch.cursorChanges:
			set.setCursor(c)
		case stopped := <-set.stopped:
			if !set.stoppedCurrent(stopped) {
				continue
			}
			// An owner error is not protection or removal evidence.
			err := set.finish(stopped.name)
			return joinErr(err, set.wait())
		}
	}
}

// virtualRenderer wraps a renderer factory for headless outputs. Vulkan is
// the only implementation of the virtual-output setting; it is not a port.
func virtualRenderer(newRenderer func(w, h int) (ports.Renderer, error)) func(w, h int) (ports.Renderer, error) {
	return func(w, h int) (ports.Renderer, error) {
		r, err := newRenderer(w, h)
		if err != nil {
			return nil, err
		}
		if v, ok := r.(interface{ SetVirtualOutput(bool) }); ok {
			v.SetVirtualOutput(true)
		}
		return r, nil
	}
}

func joinErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
