package headlessbackend

import (
	"context"
	"errors"
	"fmt"

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
	HDR   bool
}

// Run drives one virtual output per size, named HEADLESS-1, -2, ...
// Sizes may be empty: then only config-declared virtual outputs exist.
// Frame capture starts over control (Continuous). Modes of built-in heads are
// fixed; config-declared outputs reconcile through apply.
//
// newRenderer is configured for virtual outputs by the session.
//
//hexcheck:ignore suspicious-business-logic-in-adapter wires virtual output owners, forwards apply messages to core and fans out core's capture plans; preserves registration before the no-lease security barrier
func Run(ctx context.Context, o BackendOptions, apply *core.OutputApply, ch routing.Channels, curs *cursors.Router, newRenderer func(w, h int) (ports.Renderer, error), log zerowrap.Logger) error {
	set := routing.NewSet(ctx, ch.Captured, ch.Routes())
	set.Attach = ch.Content
	set.WireSecurity(ch)
	writers := map[string]chan ports.OutputContinuous{}
	inventory := domain.OutputHeads{}
	for i, size := range o.Sizes {
		name := fmt.Sprintf("HEADLESS-%d", i+1)
		mode := domain.OutputMode{Width: size[0], Height: size[1], RefreshMilli: 60000, Preferred: true}
		inventory.Heads = append(inventory.Heads, domain.OutputHead{Info: domain.OutputInfo{Name: name, Width: size[0], Height: size[1], RefreshMilli: 60000}, Modes: []domain.OutputMode{mode}, Current: &mode, Enabled: true})
		writer := make(chan ports.OutputContinuous)
		writers[name] = writer
		cur := &headless.Cursor{}
		curs.Set(name, cur)
		opts := headless.Options{Continuous: writer, Cursor: cur, LoadCursor: xcursor.LoadCursor, Width: size[0], Height: size[1], HDR: domain.OutputHDRSettings{Enabled: o.HDR}.Normalized(), Formats: ch.Formats, Log: log, NewRenderer: newRenderer, NewCaptureRenderer: newRenderer, Name: name, Presented: ch.Presented, Frames: ch.Frames, Events: ch.Events, Captured: ch.Captured}
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
	runtimeID := ports.OutputApplyIDRuntime
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
			select {
			case pending.Reply <- ports.OutputRuntimeResult{Name: pending.Name, Err: core.RuntimeOutputCode(replyNext.Err)}:
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
			if !handleContinuous(ctx, writers, host, set, req) {
				return set.Wait()
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
				select {
				case req.Reply <- ports.OutputRuntimeResult{Name: name, Err: core.RuntimeOutputCode(err)}:
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

// handleContinuous carries out core's resolved plan: every target's writer
// changes, or none. A start that fails partway stops the outputs already
// started before replying. It reports false when ctx ended.
func handleContinuous(ctx context.Context, writers map[string]chan ports.OutputContinuous, host *virtuals.Host, set *routing.Set, req ports.ContinuousCaptureRequest) bool {
	code := ""
	for _, t := range req.Targets {
		if writers[t.Output] == nil && host.Continuous(t.Output) == nil {
			// Gone since core planned: nothing has started yet.
			code = ports.ControlErrBadRequest
			break
		}
	}
	if code == "" {
		var started []string
		for _, t := range req.Targets {
			var stopped bool
			if code, stopped = sendContinuous(ctx, writers, host, set, t.Output, ports.OutputContinuous{Dir: t.Dir, Raw: req.Raw, Stop: req.Stop}); stopped {
				return false
			}
			if code != "" {
				break
			}
			started = append(started, t.Output)
		}
		if code != "" && !req.Stop {
			for _, name := range started {
				if _, stopped := sendContinuous(ctx, writers, host, set, name, ports.OutputContinuous{Stop: true}); stopped {
					return false
				}
			}
		}
	}
	select {
	case req.Reply <- ports.ContinuousCaptureResult{Dir: req.Dir, Err: code}:
	default:
	}
	return true
}

// sendContinuous hands req to the frame writer of name and waits for its
// acknowledgement. stopped reports that ctx ended.
func sendContinuous(ctx context.Context, writers map[string]chan ports.OutputContinuous, host *virtuals.Host, set *routing.Set, name string, req ports.OutputContinuous) (code string, stopped bool) {
	writer := chan<- ports.OutputContinuous(writers[name])
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
