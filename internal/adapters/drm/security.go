package drm

import (
	"context"
	"errors"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

var errSecurityScene = errors.New("scene security epoch changed or output unprepared")

// observeSecurity is output-owner only. The channel wakes; the coherent gate
// decides admission, including shutdown paths that never received the wake.
func (o *Output) observeSecurity() bool {
	if o.Security == nil {
		return false
	}
	state := o.Security.Snapshot()
	if state == o.securityState {
		return false
	}
	o.securityState = state
	o.securityPrepared = false
	o.protected = state.Protected
	o.protectSucceeded() // a new epoch gets an immediate first attempt
	if o.cursor != nil {
		o.cursor.image, o.cursor.later = false, nil
	}
	return true
}

func (o *Output) sceneCurrent(scene ports.Scene) bool {
	o.observeSecurity()
	return o.Security == nil || scene.Security == o.securityState
}

func (o *Output) invalidateSecurity() {
	o.securityPrepared = false
	if o.protected {
		o.securityInvalid = true
	}
}

func (o *Output) securityEvent(ctx context.Context, event ports.SecurityBackendEvent) error {
	if o.SecurityEvents == nil || o.Instance == 0 {
		return nil
	}
	select {
	case o.SecurityEvents <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Output) sendSecurityInvalid(ctx context.Context) error {
	if !o.securityInvalid {
		return nil
	}
	state := o.securityState
	if err := o.securityEvent(ctx, ports.SecurityOutputInvalidated{Generation: state.Generation, Instance: o.Instance}); err != nil {
		return err
	}
	o.securityInvalid = false
	return nil
}

func (o *Output) prepareSecurity(ctx context.Context, r ports.Renderer) error {
	o.observeSecurity()
	if !o.protected {
		return nil
	}
	if err := o.sendSecurityInvalid(ctx); err != nil {
		return err
	}
	state := o.securityState
	if err := o.protectedModeset(ctx, r); err != nil {
		return err
	}
	// A gate transition during the blocking clear cannot certify a new epoch.
	if o.observeSecurity() || o.securityState != state {
		return nil
	}
	o.securityPrepared = true
	kind := ports.ProtectionProtectedFrame
	if o.off {
		kind = ports.ProtectionInactiveOutput
	}
	// Separate non-merging channel; presentation feedback is not evidence.
	// Backpressure may outlive an epoch. Revalidate on each bounded send
	// attempt; the ledger also rejects a transition racing the final send.
	if o.SecurityEvents == nil || o.Instance == 0 {
		return nil
	}
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	proof := ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: state.Generation, Instance: o.Instance, Kind: kind}}
	for {
		if o.observeSecurity() || o.securityState != state {
			return nil
		}
		select {
		case o.SecurityEvents <- proof:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			timer.Reset(time.Millisecond)
		}
	}
}

// Protected frames never enter client-fence KMS paths or capture pipelines.
// Locker content is composed only after black preparation for this epoch.
func (o *Output) submitProtectedFrame(ctx context.Context, r ports.Renderer, scene ports.Scene, surfaces map[ports.WindowID]ports.SurfaceContent, seen map[ports.WindowID]uint64) error {
	if !o.securityPrepared || !o.sceneCurrent(scene) {
		return errSecurityScene
	}
	scene.CaptureScene, scene.Capture = nil, nil
	r.UseTarget(o.back)
	done, err := r.Render(scene, surfaces)
	if err != nil {
		if done != nil {
			done.Close()
		}
		return renderError{err}
	}
	// A transition while rendering must not put the obsolete scene on KMS.
	if !o.sceneCurrent(scene) || !o.protected || !o.securityPrepared {
		o.holdRead(done)
		if done != nil {
			done.Close()
		}
		return errSecurityScene
	}
	f := pendingFrame{security: scene.Security, frame: true, composed: true, shows: o.shownBy(scene, seen)}
	err = o.commitWith(o.fbs[o.back], done, false, false, f, overlayWin{})
	if err != nil {
		o.holdRead(done)
	}
	if done != nil {
		done.Close()
	}
	if err == nil {
		o.back = 1 - o.back
	}
	return err
}
