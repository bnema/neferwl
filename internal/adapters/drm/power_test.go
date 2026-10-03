package drm

import (
	"context"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// A scene with Off turns the CRTC inactive and draws nothing; the next
// scene without Off modesets (ACTIVE=1) and draws a frame.
func TestRunOutputPower(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.tearing, o.cursor = false, nil
	flips := make(chan flipEvent, 1)
	o.flipped = flips
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	renders := make(chan struct{}, 8)
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		renders <- struct{}{}
		return nil, nil
	})
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	snapshot := func() []commitRec {
		commitMu.Lock()
		defer commitMu.Unlock()
		return append([]commitRec(nil), *commits...)
	}
	active := func(c commitRec) (uint64, bool) { return c.req.value(tCrtc, pActive) }
	scenes := make(chan ports.Scene, 1)
	vt := make(chan bool)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, vt, scenes, make(chan ports.SurfaceContent), nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	on := ports.Scene{OutputWidth: 200, OutputHeight: 100, Scale: 1}
	scenes <- on
	<-renders
	var frame commitRec
	waitFor(t, func() bool {
		for _, c := range snapshot() {
			if c.user&3 == userFrame {
				frame = c
				return true
			}
		}
		return false
	})
	flips <- eventOf(frame)

	off := on
	off.Off = true
	scenes <- off
	waitFor(t, func() bool {
		cs := snapshot()
		v, ok := active(cs[len(cs)-1])
		return ok && v == 0
	})
	n := len(snapshot())
	// Scenes while off draw nothing (renders before the power off drained).
	for len(renders) > 0 {
		<-renders
	}
	scenes <- off
	time.Sleep(20 * time.Millisecond)
	select {
	case <-renders:
		t.Fatal("rendered while off")
	default:
	}
	if len(snapshot()) != n {
		t.Fatal("committed while off")
	}

	// A VT switch back modesets while off: the CRTC stays inactive.
	vt <- false
	vt <- true
	waitFor(t, func() bool { return len(snapshot()) > n })
	time.Sleep(20 * time.Millisecond)
	for _, c := range snapshot()[n:] {
		if v, ok := active(c); ok && v != 0 && c.flags&atomicTestOnly == 0 {
			t.Fatal("VT resume turned the display on")
		}
	}
	n = len(snapshot())

	scenes <- on
	<-renders
	waitFor(t, func() bool {
		for _, c := range snapshot()[n:] {
			if v, ok := active(c); ok && v == 1 {
				return true
			}
		}
		return false
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// An output started off (it reconnected while turned off) never activates
// its CRTC before a scene turns it on.
func TestRunOutputStartOff(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.tearing, o.cursor = false, nil
	o.StartOff = true
	ready := make(chan error, 1)
	o.ready = ready
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return().Maybe()
	renders := make(chan struct{}, 8)
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		renders <- struct{}{}
		return nil, nil
	}).Maybe()
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	snapshot := func() []commitRec {
		commitMu.Lock()
		defer commitMu.Unlock()
		return append([]commitRec(nil), *commits...)
	}
	lit := func(cs []commitRec) bool {
		for _, c := range cs {
			if v, ok := c.req.value(tCrtc, pActive); ok && v != 0 && c.flags&atomicTestOnly == 0 {
				return true
			}
		}
		return false
	}
	scenes := make(chan ports.Scene, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, make(chan ports.SurfaceContent), nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	for len(renders) > 0 { // the images are cleared at setup
		<-renders
	}
	off := ports.Scene{OutputWidth: 200, OutputHeight: 100, Scale: 1, Off: true}
	scenes <- off
	time.Sleep(20 * time.Millisecond)
	if lit(snapshot()) {
		t.Fatal("a display started off lit up")
	}
	if len(renders) > 0 {
		t.Fatal("rendered while off")
	}
	on := off
	on.Off = false
	scenes <- on
	waitFor(t, func() bool { return lit(snapshot()) })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
