package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/neferwl/internal/adapters/headless"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// headlessOptions are the settings of the virtual outputs.
type headlessOptions struct {
	sizes [][2]int
	shots string
	hdr   bool
	raw   bool // also write latest-pq.png (headless.Options.RawHDR)
}

// runHeadless drives one virtual output per size, named HEADLESS-1, -2, ...
// With several outputs, screenshots go to a subdirectory per output. Modes
// are fixed, so apply answers every configuration without backend work.
//
// newRenderer is configured for virtual outputs by newVulkanRenderer.
func runHeadless(ctx context.Context, o headlessOptions, apply *outputApply, ch outputChannels, curs *cursors, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	set := newOutputSet(ctx, ch.captured)
	set.wireSecurity(ch)
	inventory := ports.OutputHeads{}
	for i, size := range o.sizes {
		name := fmt.Sprintf("HEADLESS-%d", i+1)
		mode := ports.OutputMode{Width: size[0], Height: size[1], RefreshMilli: 60000, Preferred: true}
		inventory.Heads = append(inventory.Heads, ports.OutputHead{Info: ports.OutputInfo{Name: name, Width: size[0], Height: size[1], RefreshMilli: 60000}, Modes: []ports.OutputMode{mode}, Current: &mode, Enabled: true})
		dir := o.shots
		if dir != "" && len(o.sizes) > 1 {
			dir = filepath.Join(o.shots, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				// Outputs already started stop and release their captures.
				return errors.Join(err, set.wait())
			}
		}
		cur := &headless.Cursor{}
		curs.set(name, cur)
		opts := headless.Options{Cursor: cur, LoadCursor: loadCursor, Width: size[0], Height: size[1], ScreenshotDir: dir, HDR: o.hdr, RawHDR: o.raw, Formats: ch.formats, Log: log, NewRenderer: newRenderer, NewCaptureRenderer: newRenderer, Name: name, Presented: ch.presented, Captured: ch.captured}
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
	curs.move("HEADLESS-1", float64(o.sizes[0][0])/2, float64(o.sizes[0][1])/2, false)
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
			return errors.Join(err, set.wait())
		}
	}
}
