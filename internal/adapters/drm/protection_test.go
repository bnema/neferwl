package drm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func protectionFence(t *testing.T, signalled bool) (*os.File, *os.File) {
	t.Helper()
	f, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close(); w.Close() })
	if signalled {
		if _, err := w.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	return f, w
}

func checkProtectedDisable(t *testing.T, c commitRec, planes ...uint32) {
	t.Helper()
	if c.flags != atomicAllowModes || c.user != 0 {
		t.Fatalf("not blocking modeset: flags=%#x user=%d", c.flags, c.user)
	}
	if active, ok := c.req.value(tCrtc, pActive); !ok || active != 0 {
		t.Fatalf("CRTC still active: %d %v", active, ok)
	}
	// Full disable: no CRTC enabled without its primary plane (amdgpu rule).
	if v, ok := c.req.value(tCrtc, pMode); !ok || v != 0 {
		t.Fatalf("CRTC still enabled: MODE_ID %d %v", v, ok)
	}
	if v, ok := c.req.value(tConn, pConnCrtc); !ok || v != 0 {
		t.Fatalf("connector still routed: CRTC_ID %d %v", v, ok)
	}
	for _, p := range planes {
		for _, prop := range []uint32{pFB, pCrtcID} {
			if v, ok := c.req.value(p, prop); !ok || v != 0 {
				t.Fatalf("plane %d prop %d still attached: %d %v", p, prop, v, ok)
			}
		}
	}
}

// Adapted from the parent's draft: detach, clear, wait, then activate only
// the cleared target, with no nonblocking commit or desktop cursor test.
func TestProtectedModesetClearsBeforeActivation(t *testing.T) {
	o, k, commits := testOutput(t)
	o.back = 1 // exercise the other target, not just startup's back=0
	o.overlay = &plane{id: tOverlay, props: planeProps}
	o.stray = []*plane{o.overlay, {id: 53, props: planeProps}, {id: 54, props: planeProps}}
	k.EXPECT().objProps(uint32(53), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, tCrtc}}, nil)
	k.EXPECT().objProps(uint32(54), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, 99}}, nil)
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(1).Return().Once()
	f, _ := protectionFence(t, true)
	// Blob creation is part of activation and must occur after fence close.
	k.EXPECT().createBlob(mock.Anything).RunAndReturn(func([]byte) (uint32, error) {
		if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("activation before clear fence close: %v", err)
		}
		return 99, nil
	}).Once()
	r.EXPECT().Render(ports.Scene{Seq: 0, Background: "#000000"}, map[ports.WindowID]ports.SurfaceContent(nil)).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		if len(*commits) != 1 {
			t.Fatalf("clear before disable: %d commits", len(*commits))
		}
		checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor, tOverlay, 53)
		if _, ok := (*commits)[0].req.value(54, pFB); ok {
			t.Fatal("detached another output's plane")
		}
		return f, nil
	}).Once()
	if err := o.protectedModeset(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(*commits) != 2 {
		t.Fatalf("commits %d", len(*commits))
	}
	last := (*commits)[1]
	if last.flags != atomicAllowModes || last.user != 0 {
		t.Fatal("activation was not blocking")
	}
	if fb, _ := last.req.value(tPrimary, pFB); fb != 71 {
		t.Fatalf("activated fb %d, want cleared 71", fb)
	}
	if active, _ := last.req.value(tCrtc, pActive); active != 1 {
		t.Fatal("not activated")
	}
	if o.back != 0 || !o.protected || o.off {
		t.Fatalf("back=%d protected=%v off=%v", o.back, o.protected, o.off)
	}
	if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("fence not closed: %v", err)
	}
}

func TestProtectedModesetFailureNeverActivates(t *testing.T) {
	for _, stage := range []string{"disable", "render", "fence", "blob", "activate"} {
		t.Run(stage, func(t *testing.T) {
			boom := errors.New("refused")
			var failures []error
			if stage == "disable" {
				failures = []error{unix.EACCES}
			}
			if stage == "activate" {
				failures = []error{nil, unix.EPERM}
			}
			o, k, commits := testOutput(t, failures...)
			r := portsmocks.NewMockRenderer(t)
			var fence *os.File
			if stage != "disable" {
				r.EXPECT().UseTarget(0).Return().Once()
				f, w := protectionFence(t, stage != "fence")
				fence = f
				if stage == "fence" {
					w.Close() // HUP without POLLIN is not fence completion.
				}
				var renderErr error
				if stage == "render" {
					renderErr = boom
				}
				r.EXPECT().Render(mock.Anything, mock.Anything).Return(fence, renderErr).Once()
			}
			if stage == "activate" {
				k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
				k.EXPECT().destroyBlob(uint32(99)).Return(nil).Once()
			}
			if stage == "blob" {
				k.EXPECT().createBlob(mock.Anything).Return(0, boom).Once()
			}
			if err := o.protectedModeset(context.Background(), r); err == nil {
				t.Fatal("failure reported success")
			}
			if !o.protected || o.back != 0 {
				t.Fatal("failure dropped protection or flipped target")
			}
			if stage != "activate" && len(*commits) != 1 {
				t.Fatalf("activated after %s failure", stage)
			}
			if stage != "disable" && !o.off {
				t.Fatal("disabled output considered active")
			}
			if fence != nil {
				if _, err := fence.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("fence not closed: %v", err)
				}
			}
		})
	}
}

func TestProtectedResumeWithoutClearStaysInactive(t *testing.T) {
	o, _, commits := testOutput(t)
	o.protected = true
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor)
	if !o.off {
		t.Fatal("resume exposed stale target")
	}
}

func TestProtectedCloseNeverRestoresSavedDesktop(t *testing.T) {
	o, k, commits := testOutput(t)
	o.protected = true
	o.saved = modeCrtc{crtcID: tCrtc, fbID: 123, modeValid: 1, mode: o.mode}
	o.overlay = &plane{id: tOverlay, props: planeProps}
	o.stray = []*plane{o.overlay, {id: 53, props: planeProps}, {id: 54, props: planeProps}}
	k.EXPECT().objProps(uint32(53), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, tCrtc}}, nil).Once()
	k.EXPECT().objProps(uint32(54), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, 99}}, nil).Once()
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	o.Close()
	if len(*commits) != 1 {
		t.Fatalf("commits %d", len(*commits))
	}
	checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor, tOverlay, 53)
	if _, ok := (*commits)[0].req.value(54, pFB); ok {
		t.Fatal("close detached another output's plane")
	}
}

func TestProtectedModesetPendingDoesNotWaitForClientFence(t *testing.T) {
	o, _, commits := testOutput(t)
	f, _ := protectionFence(t, false)
	o.frame.begin(pendingFrame{fences: []*os.File{f}}, time.Now())
	r := portsmocks.NewMockRenderer(t)
	for _, call := range []func() error{
		func() error { return o.protectedModeset(context.Background(), r) },
		o.modeset,
	} {
		if err := call(); !errors.Is(err, errProtectionPending) {
			t.Fatalf("pending result: %v", err)
		}
	}
	if !o.protected || len(*commits) != 0 || !o.frame.pendingCommit() {
		t.Fatal("pending protection touched KMS or dropped pending state")
	}
	if _, err := f.Stat(); err != nil {
		t.Fatalf("client fence ownership dropped: %v", err)
	}
}

func TestProtectedModesetCanceled(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before-disable", false: "during-wait"}[before], func(t *testing.T) {
			o, _, commits := testOutput(t)
			r := portsmocks.NewMockRenderer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var fence *os.File
			if before {
				cancel()
			} else {
				fence, _ = protectionFence(t, false)
				r.EXPECT().UseTarget(0).Return().Once()
				r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
					cancel()
					return fence, nil
				}).Once()
			}
			if err := o.protectedModeset(ctx, r); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel result: %v", err)
			}
			want := 1
			if before {
				want = 0
			}
			if len(*commits) != want || !o.protected || o.back != 0 {
				t.Fatal("cancel activated or dropped latch")
			}
			if fence != nil {
				if !o.off {
					t.Fatal("canceled clear left output marked active")
				}
				if _, err := fence.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("cancel did not close fence: %v", err)
				}
			}
		})
	}
}

func TestProtectedModesetPreservesPowerOff(t *testing.T) {
	o, _, commits := testOutput(t)
	o.wantOff = true
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	f, _ := protectionFence(t, true)
	r.EXPECT().Render(ports.Scene{Seq: 0, Background: "#000000"}, map[ports.WindowID]ports.SurfaceContent(nil)).Return(f, nil).Once()
	if err := o.protectedModeset(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(*commits) != 1 || !o.wantOff || !o.off || !o.protected || o.back != 0 {
		t.Fatal("off proof activated output, flipped target or changed power intent")
	}
	checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor)
}

func TestProtectedModesetUnreadableStrayFailsClosed(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-error", true: "missing-property"}[missing], func(t *testing.T) {
			o, k, commits := testOutput(t)
			o.stray = []*plane{{id: tOverlay, props: planeProps}}
			var err error
			if !missing {
				err = unix.EACCES
			}
			k.EXPECT().objProps(uint32(tOverlay), uint32(objPlane)).Return(map[string][2]uint64{}, err).Once()
			if err := o.protectedModeset(context.Background(), portsmocks.NewMockRenderer(t)); err == nil {
				t.Fatal("unverified stray plane reported success")
			}
			if !o.protected || len(*commits) != 0 {
				t.Fatal("unverified detachment touched KMS or dropped latch")
			}
		})
	}
}
