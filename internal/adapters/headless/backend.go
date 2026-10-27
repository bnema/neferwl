package headless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/xcursor"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// BackendOptions are the settings of the virtual outputs.
type BackendOptions struct {
	Sizes [][2]int
	Shots string
	HDR   bool
	Raw   bool // also write latest-pq.png (Options.RawHDR)
}

// Run drives one virtual output per size, named HEADLESS-1, -2, ...
// With several outputs, screenshots go to a subdirectory per output. Modes
// are fixed, so apply answers every configuration without backend work.
//
// newRenderer is configured for virtual outputs by the session.
func Run(ctx context.Context, o BackendOptions, apply *core.OutputApply, ch routing.Channels, curs *cursors.Router, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	set := routing.NewSet(ctx, ch.Captured, ch.Routes())
	set.Attach = ch.Content
	set.WireSecurity(ch)
	inventory := domain.OutputHeads{}
	for i, size := range o.Sizes {
		name := fmt.Sprintf("HEADLESS-%d", i+1)
		mode := domain.OutputMode{Width: size[0], Height: size[1], RefreshMilli: 60000, Preferred: true}
		inventory.Heads = append(inventory.Heads, domain.OutputHead{Info: domain.OutputInfo{Name: name, Width: size[0], Height: size[1], RefreshMilli: 60000}, Modes: []domain.OutputMode{mode}, Current: &mode, Enabled: true})
		dir := o.Shots
		if dir != "" && len(o.Sizes) > 1 {
			dir = filepath.Join(o.Shots, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				// Outputs already started stop and release their captures.
				return errors.Join(err, set.Wait())
			}
		}
		cur := &Cursor{}
		curs.Set(name, cur)
		opts := Options{Cursor: cur, LoadCursor: xcursor.LoadCursor, Width: size[0], Height: size[1], ScreenshotDir: dir, HDR: o.HDR, RawHDR: o.Raw, Formats: ch.Formats, Log: log, NewRenderer: newRenderer, NewCaptureRenderer: newRenderer, Name: name, Presented: ch.Presented, Frames: ch.Frames, Captured: ch.Captured}
		started := set.Start(ctx, name, func(octx context.Context, feed routing.Feed) error {
			opts.Security, opts.SecurityChanges, opts.Instance = ch.Security, feed.Security, feed.Instance
			return RunOutput(octx, opts, feed.Scenes, feed.Contents, feed.Returns, feed.Cursor, feed.Captures)
		}, &opts.SecurityEvents)
		if !started {
			return set.Wait()
		}
		select {
		case ch.Events <- ports.OutputAdded{Info: domain.OutputInfo{Name: name, Width: size[0], Height: size[1]}}:
		case <-ctx.Done():
			return set.Wait()
		}
	}
	// All virtual lifetimes have registered before this no-lease barrier.
	if set.State.Protected {
		set.SecurityEvent(ports.SecurityBackendBarrier{Generation: set.State.Generation})
	}
	apply.Heads(inventory)
	// The pointer starts centred on the first output, like libinput's.
	curs.Move("HEADLESS-1", float64(o.Sizes[0][0])/2, float64(o.Sizes[0][1])/2, false)
	for {
		configs, configNext := apply.ConfigOut(ch.Configured), apply.Config()
		replies, replyNext := apply.ReplyOut(ch.Replies)
		heads, headsNext := apply.HeadsOut(ch.Heads)
		select {
		case <-ctx.Done():
			return set.Wait()
		case state, ok := <-ch.SecurityChanges:
			if !ok {
				ch.SecurityChanges = nil
				continue
			}
			if set.SetSecurity(state) && set.State.Protected {
				set.SecurityEvent(ports.SecurityBackendBarrier{Generation: set.State.Generation})
			}
		case ev := <-ch.Reloads:
			apply.Reload(ev.Config)
		case req := <-ch.Requests:
			apply.Request(req)
		case configs <- configNext:
			apply.ConfigSent()
		case replies <- replyNext:
			if replyNext.Err != nil {
				log.Warn().Err(replyNext.Err).Msg("output configuration failed")
			}
			apply.ReplySent()
		case heads <- headsNext:
			apply.HeadsSent()
		case stopped := <-set.Stopped:
			if !set.StoppedCurrent(stopped) {
				continue
			}
			// An owner error is not protection or removal evidence.
			err := set.Finish(stopped.Name)
			return errors.Join(err, set.Wait())
		}
	}
}
