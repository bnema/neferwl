package drm

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// runProtectedStartup runs o with the session already protected. k and r are
// set up with permissive renderer/KMS expectations; inject holds the failure
// under test.
func runProtectedStartup(t *testing.T, o *Output, k *mockkms, renders func() (*os.File, error)) (<-chan ports.SecurityBackendEvent, <-chan error, context.CancelFunc) {
	t.Helper()
	o.cursor = nil
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	events := make(chan ports.SecurityBackendEvent, 32)
	o.SecurityEvents = events
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w := protectionFence(t, false)
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return().Maybe()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		return renders()
	}).Maybe()
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, nil, nil, nil, nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	return events, done, cancel
}

func waitProof(t *testing.T, events <-chan ports.SecurityBackendEvent, done <-chan error) {
	t.Helper()
	timeout := time.After(4 * time.Second)
	for {
		select {
		case ev := <-events:
			if _, ok := ev.(ports.SecurityOutputProof); ok {
				return
			}
		case err := <-done:
			t.Fatalf("output stopped while protected: %v", err)
		case <-timeout:
			t.Fatal("no proof after retry")
		}
	}
}

// A protected startup whose first KMS attempt is refused (EBUSY,
// or a swallowed EINVAL refusal) keeps the output, backs off from that first
// refusal, and proves only after the accepted retry.
func TestProtectedStartupRefusalRetriesWithBackoffFromFirstRefusal(t *testing.T) {
	for _, errno := range []error{unix.EBUSY, unix.EINVAL} {
		t.Run(errno.Error(), func(t *testing.T) {
			// Commits: test-only modeset, then the blocking disable (refused).
			o, k, commits, mu := testOutputMu(t, nil, errno)
			k.EXPECT().createBlob(mock.Anything).Return(99, nil).Maybe()
			k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
			events, done, cancel := runProtectedStartup(t, o, k, func() (*os.File, error) { return nil, nil })
			defer cancel()
			waitProof(t, events, done)
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("output stopped: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			var real []commitRec
			for _, c := range *commits {
				if c.flags&atomicTestOnly == 0 {
					real = append(real, c)
				}
			}
			if len(real) < 3 {
				t.Fatalf("real commits %d", len(real))
			}
			if gap := real[1].at.Sub(real[0].at); gap < protectRetryMin {
				t.Fatalf("retry after %v of the first refusal, want >= %v", gap, protectRetryMin)
			}
		})
	}
}

// Lost master at protected startup keeps the output running, dark,
// without proof; it follows the lost-master invalidate path.
func TestProtectedStartupLostMasterStaysRunningWithoutProof(t *testing.T) {
	for _, errno := range []error{unix.EACCES, unix.EPERM} {
		t.Run(errno.Error(), func(t *testing.T) {
			o, k, _, _ := testOutputMu(t, nil, errno)
			k.EXPECT().createBlob(mock.Anything).Return(99, nil).Maybe()
			k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
			events, done, cancel := runProtectedStartup(t, o, k, func() (*os.File, error) { return nil, nil })
			defer cancel()
			select {
			case err := <-done:
				t.Fatalf("output stopped on lost master: %v", err)
			case ev := <-events:
				if _, ok := ev.(ports.SecurityOutputProof); ok {
					t.Fatal("proof after lost master")
				}
			case <-time.After(300 * time.Millisecond):
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("output stopped: %v", err)
			}
			if o.securityPrepared || !o.protected {
				t.Fatalf("prepared=%v protected=%v", o.securityPrepared, o.protected)
			}
		})
	}
}

// Startup failures of detachment, mode blob or clear fence keep
// the output and recover through the bounded retry; proof only after success.
func TestProtectedStartupRecoverableFailuresRetry(t *testing.T) {
	for _, stage := range []string{"detach", "blob", "fence"} {
		t.Run(stage, func(t *testing.T) {
			o, k, _, _ := testOutputMu(t)
			var failures, blobs, objs, renders atomic.Int32
			switch stage {
			case "detach":
				o.stray = []*plane{{id: 53, props: planeProps}}
				k.EXPECT().objProps(uint32(53), uint32(objPlane)).RunAndReturn(func(uint32, uint32) (map[string][2]uint64, error) {
					// 1: test modeset request (error ignored); 2: first detach.
					if objs.Add(1) == 2 {
						failures.Add(1)
						return nil, unix.EIO
					}
					return map[string][2]uint64{"CRTC_ID": {pCrtcID, 0}}, nil
				}).Maybe()
				k.EXPECT().createBlob(mock.Anything).Return(99, nil).Maybe()
			case "blob":
				k.EXPECT().createBlob(mock.Anything).RunAndReturn(func([]byte) (uint32, error) {
					if blobs.Add(1) == 2 { // 1: test modeset; 2: activation
						failures.Add(1)
						return 0, unix.ENOMEM
					}
					return 99, nil
				}).Maybe()
			case "fence":
				k.EXPECT().createBlob(mock.Anything).Return(99, nil).Maybe()
			}
			k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
			events, done, cancel := runProtectedStartup(t, o, k, func() (*os.File, error) {
				// Renders 1-2 initialise the images; 3 is the first protected clear.
				if stage != "fence" || renders.Add(1) != 3 {
					return nil, nil
				}
				failures.Add(1)
				f, w := protectionFence(t, false)
				w.Close() // HUP without POLLIN is an error, not completion
				return f, nil
			})
			defer cancel()
			waitProof(t, events, done)
			if failures.Load() == 0 {
				t.Fatal("failure was not injected before proof")
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("output stopped: %v", err)
			}
		})
	}
}

// Recoverable preparation failures are retryable protected errors
// handled by commitFailed with backoff; a renderer error stays fatal.
func TestProtectedPreparationFailuresRetryableRenderFatal(t *testing.T) {
	boom := errors.New("render broke")
	for _, stage := range []string{"stray-read", "stray-missing", "blob", "fence", "render"} {
		t.Run(stage, func(t *testing.T) {
			o, k, _ := testOutput(t)
			r := portsmocks.NewMockRenderer(t)
			switch stage {
			case "stray-read", "stray-missing":
				o.stray = []*plane{{id: 53, props: planeProps}}
				var err error
				props := map[string][2]uint64{}
				if stage == "stray-read" {
					err = unix.EIO
				}
				k.EXPECT().objProps(uint32(53), uint32(objPlane)).Return(props, err).Once()
			case "blob":
				k.EXPECT().createBlob(mock.Anything).Return(0, unix.ENOMEM).Once()
				r.EXPECT().UseTarget(0).Return().Once()
				r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
			case "fence":
				f, w := protectionFence(t, false)
				w.Close()
				r.EXPECT().UseTarget(0).Return().Once()
				r.EXPECT().Render(mock.Anything, mock.Anything).Return(f, nil).Once()
			case "render":
				r.EXPECT().UseTarget(0).Return().Once()
				r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, boom).Once()
			}
			o.protected = true
			err := o.protectedModeset(context.Background(), r)
			if err == nil {
				t.Fatal("failure reported success")
			}
			enabled := true
			handled := o.commitFailed(err, &enabled)
			if stage == "render" {
				if retryableProtected(err) || handled {
					t.Fatalf("renderer error must stay fatal: retryable=%v handled=%v", retryableProtected(err), handled)
				}
				return
			}
			if !retryableProtected(err) || !handled || !enabled || !o.protectBackoffActive() || o.securityPrepared {
				t.Fatalf("not retried with backoff: %v", err)
			}
		})
	}
}

// A protected EBUSY/lost-master/detach error from showImages is a
// protected KMS error: it never triggers the HDR->SDR fallback or hdrFailed.
func TestProtectedShowImagesHDRKeepsHDRForKMSErrors(t *testing.T) {
	for _, errno := range []error{unix.EBUSY, unix.EACCES, unix.EPERM, unix.EINVAL, nil} {
		name := "detach"
		if errno != nil {
			name = errno.Error()
		}
		t.Run(name, func(t *testing.T) {
			var errs []error
			if errno != nil {
				errs = []error{nil, errno} // HDR test commit, then the disable
			}
			o, k, _ := testOutput(t, errs...)
			o.cursor = nil
			o.protected = true
			o.hdr.cap = hdrCapability{Capable: true}
			o.hdr.settings = HDRSettings{Enabled: true, SDRBrightness: 203}
			o.hdr.props = connectorHDRProps{Metadata: 100, Colorspace: 101, MaxBPC: 102, BT2020Value: 7, HasDefault: true, MaxBPCValue: 8}
			o.primary.formats = []ports.DMABufFormat{{Format: fourccXR30, Modifier: 0}}
			if errno == nil {
				o.primary.props = map[string]uint32{"CRTC_ID": pCrtcID} // incomplete: detach fails
			}
			r := portsmocks.NewMockRenderer(t)
			buf := func() ports.DMABuf {
				f, w := protectionFence(t, false)
				w.Close()
				return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
			}
			r.EXPECT().SetHDR(float64(203)).Return().Once()
			r.EXPECT().UseTarget(mock.Anything).Return().Maybe()
			r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
			r.EXPECT().ExportTargets(2, []uint64{0}).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
			k.EXPECT().addFB(mock.Anything, uint32(fourccXR30)).Return(70, nil).Maybe()
			k.EXPECT().createBlob(mock.Anything).Return(321, nil).Maybe()
			k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
			err := o.showImages(r, imagesDriver, nil)
			if err == nil {
				t.Fatal("protected KMS failure reported success")
			}
			enabled := true
			lost := errno != nil && lostMaster(errno)
			if !o.commitFailed(err, &enabled) {
				t.Fatalf("protected failure not handled: %v", err)
			}
			if o.hdr.failed || !o.hdr.on {
				t.Fatalf("HDR fell back: failed=%v on=%v", o.hdr.failed, o.hdr.on)
			}
			if lost == enabled {
				t.Fatalf("lost master=%v enabled=%v", lost, enabled)
			}
			if !o.protected || o.securityPrepared {
				t.Fatal("protection dropped or certified")
			}
		})
	}
}
