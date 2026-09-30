package drm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// errProtectionPending asks the output owner to retry after the outstanding
// commit ends. In particular, never queue a blocking disable behind a client
// acquire fence: that fence need not signal in bounded time.
var errProtectionPending = errors.New("output protection: commit pending")

// detachProtectedPlanes detaches every plane owned by this output, and stray
// planes currently on its CRTC (not planes belonging to another output). A
// failed read is not evidence that a stray plane is detached.
func (o *Output) detachProtectedPlanes(req *atomicReq) error {
	var err error
	owned := make(map[uint32]bool)
	detach := func(p *plane) {
		if p == nil {
			return
		}
		// atomicReq.set silently skips zero property IDs. Partial cached
		// metadata therefore cannot prove detachment of a previous desktop.
		if p.id == 0 || p.prop("FB_ID") == 0 || p.prop("CRTC_ID") == 0 {
			err = errors.Join(err, fmt.Errorf("protected plane %d: incomplete detachment properties", p.id))
			return
		}
		req.set(p.id, p.prop("FB_ID"), 0)
		req.set(p.id, p.prop("CRTC_ID"), 0)
	}
	detachOwned := func(p *plane) {
		if p != nil {
			owned[p.id] = true
			detach(p)
		}
	}
	detachOwned(o.primary)
	if o.cursor != nil {
		detachOwned(o.cursor.plane)
	}
	detachOwned(o.overlay)
	for _, p := range o.stray {
		// pickOverlay leaves the owned overlay in stray. Do not turn a
		// redundant query failure into refusal of known-owned detachment.
		if owned[p.id] {
			continue
		}
		props, readErr := o.k.objProps(p.id, objPlane)
		if readErr != nil {
			err = errors.Join(err, fmt.Errorf("read protected plane %d: %w", p.id, readErr))
			continue
		}
		crtc, ok := props["CRTC_ID"]
		if !ok {
			err = errors.Join(err, fmt.Errorf("protected plane %d: missing CRTC_ID", p.id))
			continue
		}
		if crtc[1] == uint64(o.crtc) {
			detach(p)
		}
	}
	return err
}

// disableProtected is the atomic disable of a protected output. It is a full
// disable: ACTIVE=0, MODE_ID=0, connector CRTC_ID=0, every plane detached, VRR
// and HDR connector state off. amdgpu (amdgpu_dm, via
// drm_atomic_helper_check_crtc_primary_plane) rejects with EINVAL a CRTC that
// stays enabled (MODE_ID set) without its primary plane, even with ACTIVE=0,
// so "inactive but every plane detached" is not a valid request. The other
// valid shape, ACTIVE=0 with the primary still attached, would keep a desktop
// framebuffer referenced by the CRTC until a later activation replaced it. A
// full disable leaves nothing of the desktop attached and needs no blob; the
// activation that follows is a full modeset either way. It is the same
// request as Close's disable, which this hardware accepts.
func (o *Output) disableProtected() error {
	req := &atomicReq{}
	req.set(o.crtc, o.crtcProps["ACTIVE"], 0)
	req.set(o.crtc, o.crtcProps["MODE_ID"], 0)
	req.set(o.crtc, o.vrrProp, 0)
	req.set(o.conn.id, o.connCrtc, 0)
	o.hdrConnectorProps(req, false)
	if err := o.detachProtectedPlanes(req); err != nil {
		return protectedCommitError{err}
	}
	if err := o.k.commit(req, atomicAllowModes, 0); err != nil {
		return protectedCommitError{fmt.Errorf("disable protected output: %w", err)}
	}
	if o.modeBlob != 0 {
		_ = o.k.destroyBlob(o.modeBlob) // MODE_ID is 0 now: nothing references it
		o.modeBlob = 0
	}
	o.off, o.vrrOn, o.vrrGame, o.overlayOn = true, false, false, 0
	o.sendFormats()
	o.shown, o.queued = 0, 0
	if o.cursor != nil {
		o.cursor.applied = cursorState{}
		o.cursor.screen, o.cursor.flying = 0, false
	}
	return nil
}

// protectedCommitError marks a recoverable failure of the protected disable or
// activation: a refused KMS commit, unverifiable plane state, a mode blob or a
// clear fence that failed. The output owner keeps the output, stays protected
// and retries with bounded backoff instead of stopping (see commitFailed). A
// renderer error is not wrapped: it stays fatal.
type protectedCommitError struct{ error }

func (e protectedCommitError) Unwrap() error { return e.error }

// retryableProtected reports a refused protected KMS commit worth retrying
// later. Busy and lost-master errors keep their own handling.
func retryableProtected(err error) bool {
	var pe protectedCommitError
	return errors.As(err, &pe) && !lostMaster(err) && !errors.Is(err, unix.EBUSY)
}

const (
	protectRetryMin = 50 * time.Millisecond
	protectRetryMax = 2 * time.Second
)

// deferProtection schedules the next protected attempt after a refusal.
func (o *Output) deferProtection() {
	o.protectBackoff = nextProtectBackoff(o.protectBackoff)
	o.protectNotBefore = time.Now().Add(o.protectBackoff)
}

// protectBackoffActive reports a pending retry delay.
func (o *Output) protectBackoffActive() bool {
	return !o.protectNotBefore.IsZero() && time.Now().Before(o.protectNotBefore)
}

// protectSucceeded clears the retry state.
func (o *Output) protectSucceeded() { o.protectBackoff, o.protectNotBefore = 0, time.Time{} }

// nextProtectBackoff doubles the delay, bounded by protectRetryMax.
func nextProtectBackoff(d time.Duration) time.Duration {
	if d < protectRetryMin {
		return protectRetryMin
	}
	return min(2*d, protectRetryMax)
}

// protectedModeset is an internal output-owner primitive, not session-lock
// readiness or a protocol entry point. It latches protection even on failure.
// First stop scanout with a blocking full disable, then clear an unscanned
// target and wait for the clear to complete before activating that exact FB.
func (o *Output) protectedModeset(ctx context.Context, r ports.Renderer) error {
	o.protected = true
	if o.frame.pendingCommit() {
		return errProtectionPending
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.disableProtected(); err != nil {
		return err
	}
	// Do not dropRead here: an earlier render whose KMS commit failed may
	// still read client buffers. KMS detachment is not a GPU completion fence.
	if err := ctx.Err(); err != nil {
		return err
	}
	r.UseTarget(o.back)
	fence, err := r.Render(ports.Scene{Seq: 0, Background: "#000000"}, nil)
	if err != nil {
		if fence != nil {
			fence.Close()
		}
		return fmt.Errorf("clear protected output: %w", err)
	}
	if fence != nil {
		err := syncfile.Wait(ctx, fence)
		fence.Close()
		if err != nil {
			err = fmt.Errorf("wait protected clear: %w", err)
			if ctx.Err() != nil {
				return err
			}
			return protectedCommitError{err}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.wantOff {
		o.protectSucceeded()
		o.signalReady()
		return nil // the blocking disable is the proof; do not activate or flip
	}
	if err := o.modesetImage(o.fbs[o.back], true); err != nil {
		return err
	}
	o.back = 1 - o.back
	o.protectSucceeded()
	return nil
}
