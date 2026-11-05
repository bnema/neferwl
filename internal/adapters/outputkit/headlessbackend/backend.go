package headlessbackend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/headless"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/outputkit/virtuals"
	"github.com/bnema/neferwl/internal/adapters/xcursor"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// BackendOptions are the settings of the virtual outputs.
type BackendOptions struct {
	Sizes [][2]int
	Shots string
	HDR   bool
	Raw   bool // also write latest-pq.png (Options.RawHDR)
}

// Run drives one virtual output per size, named HEADLESS-1, -2, ...
// Sizes may be empty: then only config-declared virtual outputs exist.
// With several outputs, screenshots go to a subdirectory per output. Modes
// of built-in heads are fixed; config-declared outputs reconcile through apply.
//
// newRenderer is configured for virtual outputs by the session.
//
//hexcheck:ignore suspicious-business-logic-in-adapter wires virtual output owners and forwards apply messages to core; preserves registration before the no-lease security barrier
func Run(ctx context.Context, o BackendOptions, apply *core.OutputApply, ch routing.Channels, curs *cursors.Router, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	set := routing.NewSet(ctx, ch.Captured, ch.Routes())
	set.Attach = ch.Content
	set.WireSecurity(ch)
	writers := map[string]chan ports.ContinuousCaptureRequest{}
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
		writer := make(chan ports.ContinuousCaptureRequest)
		writers[name] = writer
		cur := &headless.Cursor{}
		curs.Set(name, cur)
		opts := headless.Options{Continuous: writer, Cursor: cur, LoadCursor: xcursor.LoadCursor, Width: size[0], Height: size[1], ScreenshotDir: dir, HDR: domain.OutputHDRSettings{Enabled: o.HDR}.Normalized(), RawHDR: o.Raw, Formats: ch.Formats, Log: log, NewRenderer: newRenderer, NewCaptureRenderer: newRenderer, Name: name, Presented: ch.Presented, Frames: ch.Frames, Events: ch.Events, Captured: ch.Captured}
		if !headless.StartOutput(ctx, set, ch, domain.OutputInfo{Name: name, Width: size[0], Height: size[1]}, opts) {
			return set.Wait()
		}
	}
	// Publish fixed heads first so core rejects virtual name collisions.
	apply.Heads(inventory)
	host := virtuals.New(set, ch, curs, newRenderer, log)
	publish := func() {
		heads := domain.OutputHeads{Heads: append([]domain.OutputHead(nil), inventory.Heads...)}
		heads.Heads = append(heads.Heads, host.Heads()...)
		apply.Heads(heads)
	}
	if err := host.Sync(ctx, apply.Virtuals()); err != nil {
		return errors.Join(err, set.Wait())
	}
	publish()
	// All virtual lifetimes have registered before this no-lease barrier.
	if set.State.Protected {
		set.SecurityEvent(ports.SecurityBackendBarrier{Generation: set.State.Generation})
	}
	// The pointer starts centred on the first output, like libinput's. With
	// virtual outputs only, there is no fixed head to centre on.
	if len(o.Sizes) > 0 {
		curs.Move("HEADLESS-1", float64(o.Sizes[0][0])/2, float64(o.Sizes[0][1])/2, false)
	}
	runtimeReplies := map[uint64]ports.OutputRuntimeRequest{}
	var runtimeID uint64 = 1 << 63
	for {
		if _, ok := apply.Next(); ok {
			err := host.Sync(ctx, apply.Virtuals())
			publish()
			apply.Finished(err)
			continue
		}
		for _, warning := range apply.TakeWarnings() {
			log.Warn().Str("output", warning.Name).Msg(warning.Message)
		}
		configs, configNext := apply.ConfigOut(ch.Configured), apply.Config()
		replies, replyNext := apply.ReplyOut(ch.Replies)
		heads, headsNext := apply.HeadsOut(ch.Heads)
		if pending, ok := runtimeReplies[replyNext.ID]; ok {
			code := ""
			if replyNext.Err != nil {
				code = ports.ControlErrBadRequest
			}
			select {
			case pending.Reply <- ports.OutputRuntimeResult{Name: pending.Name, Err: code}:
			default:
			}
			select {
			case pending.Wake <- struct{}{}:
			default:
			}
			delete(runtimeReplies, replyNext.ID)
			apply.ReplySent()
			continue
		}
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
		case req := <-ch.Continuous:
			code := ""
			if ch.Security != nil && ch.Security.Snapshot().Protected {
				code = ports.ControlErrLocked
			}
			names := req.Outputs
			if len(names) == 0 {
				for _, head := range inventory.Heads {
					names = append(names, head.Info.Name)
				}
				for _, head := range host.Heads() {
					names = append(names, head.Info.Name)
				}
			}
			for _, name := range names {
				if code == "" && writers[name] == nil && host.Continuous(name) == nil {
					code = ports.ControlErrBadRequest
				}
			}
			// Frames go to a directory the session chose, never relative to
			// the compositor's working directory.
			if code == "" && !req.Stop && !filepath.IsAbs(req.Dir) {
				code = ports.ControlErrBadRequest
			}
			if code == "" {
				var started []string
				for _, name := range names {
					local := req
					if len(names) > 1 && !req.Stop {
						local.Dir = filepath.Join(req.Dir, name)
					}
					var stopped bool
					if code, stopped = sendContinuous(ctx, writers, host, set, name, local); stopped {
						return set.Wait()
					}
					if code != "" {
						break
					}
					started = append(started, name)
				}
				// A partial start stops the outputs already writing frames.
				if code != "" && !req.Stop {
					for _, name := range started {
						if _, stopped := sendContinuous(ctx, writers, host, set, name, ports.ContinuousCaptureRequest{Stop: true}); stopped {
							return set.Wait()
						}
					}
				}
			}
			select {
			case req.Reply <- ports.ContinuousCaptureResult{Dir: req.Dir, Err: code}:
			default:
			}
		case req := <-ch.RuntimeOutputs:
			runtimeID++
			name := req.Name
			var err error
			if req.Remove {
				err = apply.RuntimeRemove(runtimeID, name)
			} else {
				name, err = apply.RuntimeAdd(runtimeID, name, req.Mode)
			}
			if err != nil {
				code := ports.ControlErrBadRequest
				if errors.Is(err, core.ErrRuntimeOutputForbidden) {
					code = ports.ControlErrForbidden
				}
				select {
				case req.Reply <- ports.OutputRuntimeResult{Name: name, Err: code}:
				default:
				}
				select {
				case req.Wake <- struct{}{}:
				default:
				}
			} else {
				req.Name = name
				runtimeReplies[runtimeID] = req
			}
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
			if host.Owns(stopped.Name) {
				err := host.Remove(ctx, stopped.Name)
				publish()
				if err != nil {
					log.Error().Err(err).Str("output", stopped.Name).Msg("virtual output stopped")
				}
				continue
			}
			// An owner error is not protection or removal evidence.
			err := set.Finish(stopped.Name)
			return errors.Join(err, set.Wait())
		}
	}
}

// sendContinuous hands req to the frame writer of name and waits for its
// acknowledgement. stopped reports that ctx ended.
func sendContinuous(ctx context.Context, writers map[string]chan ports.ContinuousCaptureRequest, host *virtuals.Host, set *routing.Set, name string, req ports.ContinuousCaptureRequest) (code string, stopped bool) {
	writer := chan<- ports.ContinuousCaptureRequest(writers[name])
	if writer == nil {
		writer = host.Continuous(name)
	}
	ack := make(chan ports.ContinuousCaptureResult, 1)
	req.Reply = ack
	select {
	case writer <- req:
	case <-set.Outs[name].Done:
		return ports.ControlErrCaptureFailed, false
	case <-ctx.Done():
		return "", true
	}
	select {
	case result := <-ack:
		return result.Err, false
	case <-set.Outs[name].Done:
		return ports.ControlErrCaptureFailed, false
	case <-ctx.Done():
		return "", true
	}
}
