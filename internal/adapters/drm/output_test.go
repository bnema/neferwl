package drm

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	portsmocks "github.com/bnema/nefertty/internal/mocks/ports"
	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// Object and property IDs of the test output.
const (
	tCrtc, tConn, tPrimary, tCursor, tOverlay = 30, 40, 50, 51, 52
	pFB, pCrtcID, pFence, pMode, pActive      = 1, 2, 3, 4, 5
	pVRR, pConnCrtc                           = 6, 7
)

var planeProps = map[string]uint32{"FB_ID": pFB, "CRTC_ID": pCrtcID, "IN_FENCE_FD": pFence, "SRC_X": 10, "SRC_Y": 11, "SRC_W": 12, "SRC_H": 13, "CRTC_X": 14, "CRTC_Y": 15, "CRTC_W": 16, "CRTC_H": 17}

// commitMu guards recorded commits (Run commits on its goroutine).
var commitMu sync.Mutex

// commitRec is one recorded atomic commit.
type commitRec struct {
	req   atomicReq
	flags uint32
	user  uint64
}

// testOutput returns an output on a mocked kms that records commits;
// commit returns the next error of errs (nil when empty).
func testOutput(t *testing.T, errs ...error) (*Output, *mockkms, *[]commitRec) {
	k := newMockkms(t)
	var commits []commitRec
	k.EXPECT().commit(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(r *atomicReq, flags uint32, user uint64) error {
		commitMu.Lock()
		defer commitMu.Unlock()
		c := commitRec{flags: flags, user: user}
		c.req.objs = append([]uint32(nil), r.objs...)
		for _, ps := range r.props {
			c.req.props = append(c.req.props, append([]propValue(nil), ps...))
		}
		commits = append(commits, c)
		if len(errs) > 0 {
			err := errs[0]
			errs = errs[1:]
			return err
		}
		return nil
	}).Maybe()
	primary := &plane{id: tPrimary, typ: planePrimary, props: planeProps}
	cur := newCursor(&plane{id: tCursor, typ: planeCursor, props: planeProps}, 64)
	cur.fbs = [2]uint32{90, 91}
	o := &Output{k: k, crtc: tCrtc, conn: connector{id: tConn, name: "DP-1"}, mode: modeInfo{HDisplay: 200, VDisplay: 100, VRefresh: 60}, log: zerowrap.Default(),
		crtcProps: map[string]uint32{"MODE_ID": pMode, "ACTIVE": pActive, "VRR_ENABLED": pVRR}, connCrtc: pConnCrtc, vrrProp: pVRR,
		primary: primary, cursor: cur, fbs: [2]uint32{70, 71}, clientFBs: map[uint64]*clientFB{}, scanout: true, tearing: true, asyncFence: true}
	return o, k, &commits
}

func objCount(c commitRec) int { return len(c.req.objs) }

func TestFrameCommitCarriesPrimaryFenceCursorAndVRR(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.cursor.Move(12, 34)
	fence, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	defer w.Close()
	if err := o.commitFrame(70, fence, false, true, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	c := (*commits)[0]
	if c.flags != atomicNonblock|flipEventFlag || c.user != userFrame {
		t.Fatalf("flags %#x user %d", c.flags, c.user)
	}
	if v, _ := c.req.value(tPrimary, pFB); v != 70 {
		t.Fatalf("primary fb %d", v)
	}
	if v, ok := c.req.value(tPrimary, pFence); !ok || v != uint64(fence.Fd()) {
		t.Fatalf("fence %d %v", v, ok)
	}
	if v, _ := c.req.value(tCursor, 14); v != 12 {
		t.Fatalf("cursor x %d", v)
	}
	if v, _ := c.req.value(tCrtc, pVRR); v != 1 {
		t.Fatalf("vrr %d", v)
	}
	if !o.pending || !o.vrrOn || o.cursor.applied.x != 12 {
		t.Fatalf("state pending=%v vrr=%v cursor=%+v", o.pending, o.vrrOn, o.cursor.applied)
	}
}

func TestAsyncCommitHasOnlyPrimaryFBAndFence(t *testing.T) {
	o, _, commits := testOutput(t)
	fence, w, _ := os.Pipe()
	defer fence.Close()
	defer w.Close()
	if err := o.commitFrame(70, fence, true, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	c := (*commits)[0]
	if c.flags&flipAsyncFlag == 0 {
		t.Fatal("not async")
	}
	if objCount(c) != 1 || c.req.objs[0] != tPrimary || len(c.req.props[0]) != 2 {
		t.Fatalf("async commit %+v", c.req)
	}
}

func TestAsyncFallsBackToSyncWhenCursorMoves(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.cursor.Move(5, 5)
	if err := o.commitFrame(70, nil, true, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if (*commits)[0].flags&flipAsyncFlag != 0 {
		t.Fatal("async commit moved the cursor")
	}
}

func TestAsyncWithoutFenceSupportStaysSync(t *testing.T) {
	o, _, commits := testOutput(t)
	o.asyncFence = false
	fence, w, _ := os.Pipe()
	defer fence.Close()
	defer w.Close()
	if err := o.commitFrame(70, fence, true, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if (*commits)[0].flags&flipAsyncFlag != 0 {
		t.Fatal("async commit with a fence the driver refuses")
	}
}

func TestScanoutAsyncRefusedRetriesSync(t *testing.T) {
	o, _, commits := testOutput(t, unix.EINVAL)
	o.shown, o.vrrOn = 1, true
	buf := &ports.DMABuf{ID: 9}
	o.clientFBs[9] = &clientFB{fbID: 80}
	ok, err := o.commitScanout(80, ports.SurfaceContent{DMABuf: buf, Async: true}, pendingFrame{})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(*commits) != 2 || (*commits)[0].flags&flipAsyncFlag == 0 || (*commits)[1].flags&flipAsyncFlag != 0 {
		t.Fatalf("commits %+v", *commits)
	}
	if !o.clientFBs[9].noAsync || o.queued != 9 {
		t.Fatalf("noAsync=%v queued=%d", o.clientFBs[9].noAsync, o.queued)
	}
}

func TestCursorCommitWaitsForEvent(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.cursor.Move(1, 1)
	if err := o.commitState(false); err != nil {
		t.Fatal(err)
	}
	c := (*commits)[0]
	if c.flags != atomicNonblock|flipEventFlag || c.user != userState || !o.pending {
		t.Fatalf("cursor commit flags %#x user %d pending %v", c.flags, c.user, o.pending)
	}
	// Its event is not a frame: nothing is reported.
	seen := map[ports.WindowID]uint64{}
	o.completed(flipEvent{user: userState}, seen)
	if o.pending || len(o.unsent) != 0 || o.flips != 0 {
		t.Fatalf("pending=%v unsent=%d flips=%d", o.pending, len(o.unsent), o.flips)
	}
	// Nothing changed: no commit.
	if err := o.commitState(false); err != nil || len(*commits) != 1 {
		t.Fatalf("idle commit: %v, %d commits", err, len(*commits))
	}
}

func TestBusyCommitStaysPending(t *testing.T) {
	o, _, _ := testOutput(t, unix.EBUSY)
	err := o.commitFrame(70, nil, false, false, pendingFrame{})
	enabled := true
	if !o.commitFailed(err, &enabled) || !o.pending || !enabled {
		t.Fatalf("EBUSY: pending=%v enabled=%v", o.pending, enabled)
	}
}

func TestFlipReportsTimestampAndShownFrame(t *testing.T) {
	o, _, _ := testOutput(t)
	o.vrrOn = false
	o.mode.Clock, o.mode.HTotal, o.mode.VTotal = 0, 0, 0
	if err := o.commitFrame(70, nil, false, false, pendingFrame{shows: map[ports.WindowID]uint64{1: 4}}); err != nil {
		t.Fatal(err)
	}
	o.completed(flipEvent{user: userFrame, when: 5 * time.Second, seq: 11}, map[ports.WindowID]uint64{1: 4})
	if len(o.unsent) != 1 {
		t.Fatalf("reports %d", len(o.unsent))
	}
	f := o.unsent[0].Flip
	if f == nil || f.When != 5*time.Second || f.Seq != 11 || f.Refresh != time.Second/60 || f.Shows[1] != 4 || !f.HardwareClock {
		t.Fatalf("flip %+v", f)
	}
}

func TestReportsKeepEveryFlip(t *testing.T) {
	o, _, _ := testOutput(t)
	seen := map[ports.WindowID]uint64{1: 1}
	o.report(&ports.FlipInfo{Seq: 1}, seen)
	o.report(nil, seen)
	o.report(&ports.FlipInfo{Seq: 2}, seen)
	o.report(nil, map[ports.WindowID]uint64{1: 3})
	if len(o.unsent) != 2 || o.unsent[0].Flip.Seq != 1 || o.unsent[1].Flip.Seq != 2 || o.unsent[1].Seen[1] != 3 {
		t.Fatalf("unsent %+v", o.unsent)
	}
	for i := range maxUnsent + 3 {
		o.report(&ports.FlipInfo{Seq: uint64(10 + i), Shows: map[ports.WindowID]uint64{}}, seen)
	}
	if len(o.unsent) != maxUnsent || o.unsent[0].Flip.Merged == 0 {
		t.Fatalf("cap: %d reports, first merged %d", len(o.unsent), o.unsent[0].Flip.Merged)
	}
}

func TestModesetDisablesCursorAndStrayPlanes(t *testing.T) {
	o, k, commits := testOutput(t)
	stray := &plane{id: tOverlay, typ: planeOverlay, props: planeProps}
	o.stray = []*plane{stray}
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
	k.EXPECT().objProps(uint32(tOverlay), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, tCrtc}}, nil).Once()
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	c := (*commits)[0]
	if c.flags != atomicAllowModes {
		t.Fatalf("flags %#x", c.flags)
	}
	if v, _ := c.req.value(tCrtc, pMode); v != 99 {
		t.Fatalf("mode blob %d", v)
	}
	for _, p := range []uint32{tCursor, tOverlay} {
		if v, ok := c.req.value(p, pFB); !ok || v != 0 {
			t.Fatalf("plane %d fb %d %v", p, v, ok)
		}
	}
	if v, _ := c.req.value(tConn, pConnCrtc); v != tCrtc {
		t.Fatalf("connector crtc %d", v)
	}
	// The next modeset replaces the blob.
	k.EXPECT().createBlob(mock.Anything).Return(100, nil).Once()
	k.EXPECT().objProps(uint32(tOverlay), uint32(objPlane)).Return(map[string][2]uint64{}, nil).Once()
	k.EXPECT().destroyBlob(uint32(99)).Return(nil).Once()
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*commits)[1].req.value(tOverlay, pFB); ok {
		t.Fatal("plane on another CRTC turned off")
	}
}

func TestAsyncProbeAfterModeset(t *testing.T) {
	o, _, commits := testOutput(t, unix.EINVAL)
	o.asyncFence = true
	r := portsmocks.NewMockRenderer(t)
	fence, w, _ := os.Pipe()
	defer w.Close()
	r.EXPECT().UseTarget(mock.Anything).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(fence, nil).Once()
	o.probeAsync(r)
	c := (*commits)[0]
	if c.flags != atomicTestOnly|flipAsyncFlag {
		t.Fatalf("flags %#x", c.flags)
	}
	if _, ok := c.req.value(tPrimary, pFence); !ok {
		t.Fatal("probe without a fence")
	}
	if o.asyncFence {
		t.Fatal("refused probe kept asyncFence")
	}
	if _, err := fence.Stat(); err == nil {
		t.Fatal("probe fence not closed")
	}
	o.probeAsync(r) // once only
}

func TestVRRToggleOnlyInsideFrames(t *testing.T) {
	o, _, commits := testOutput(t)
	o.vrrOn = true
	o.composedSince = time.Now().Add(-time.Second)
	if o.wantVRR(false) {
		t.Fatal("VRR kept after vrrHold of composition")
	}
	o.composedSince = time.Now()
	if !o.wantVRR(false) {
		t.Fatal("VRR dropped before vrrHold")
	}
	if err := o.commitState(false); err != nil {
		t.Fatal(err)
	}
	if v, ok := (*commits)[0].req.value(tCrtc, pVRR); !ok || v != 0 || (*commits)[0].flags&flipEventFlag == 0 {
		t.Fatalf("vrr off commit %+v", (*commits)[0])
	}
}

func TestReadPlanes(t *testing.T) {
	k := newMockkms(t)
	k.EXPECT().planes().Return([]planeRes{{id: 1, possible: 1}, {id: 2, possible: 2}, {id: 3, possible: 3}}, nil)
	k.EXPECT().objProps(uint32(1), uint32(objPlane)).Return(map[string][2]uint64{"type": {20, planePrimary}, "FB_ID": {21, 0}, "IN_FORMATS": {22, 5}}, nil)
	k.EXPECT().objProps(uint32(3), uint32(objPlane)).Return(map[string][2]uint64{"type": {20, planeCursor}}, nil)
	k.EXPECT().getBlob(uint32(5)).Return(nil, nil)
	ps, err := readPlanes(k, 0)
	if err != nil || len(ps) != 2 || ps[0].typ != planePrimary || ps[0].prop("FB_ID") != 21 || ps[1].typ != planeCursor {
		t.Fatalf("planes %+v %v", ps, err)
	}
}

func TestVRRRefusedIsDisabled(t *testing.T) {
	o, _, commits := testOutput(t, unix.EINVAL)
	if err := o.commitFrame(70, nil, false, true, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if len(*commits) != 2 || o.vrrProp != 0 || !o.pending {
		t.Fatalf("%d commits, vrrProp %d", len(*commits), o.vrrProp)
	}
	if v, _ := (*commits)[1].req.value(tCrtc, pVRR); v != 0 {
		t.Fatal("retry kept VRR on")
	}
}

func TestBufferRefusedIsNotBlamedOnVRR(t *testing.T) {
	o, _, _ := testOutput(t, unix.EINVAL, unix.EINVAL)
	o.clientFBs[9] = &clientFB{fbID: 80}
	ok, err := o.commitScanout(80, ports.SurfaceContent{DMABuf: &ports.DMABuf{ID: 9}}, pendingFrame{})
	if ok || err != nil || o.vrrProp == 0 || o.clientFBs[9].failed != "flip_refused" {
		t.Fatalf("ok=%v err=%v vrrProp=%d failed=%q", ok, err, o.vrrProp, o.clientFBs[9].failed)
	}
}

// Run: while a frame is in flight its buffers may still be read by the
// GPU, so a newer content is reported only after the flip event; every
// frame fence is closed.
func TestRunReportsSeenAfterFlip(t *testing.T) {
	o, k, commits := testOutput(t)
	o.cursor, o.tearing = nil, false
	flips := make(chan flipEvent, 1)
	o.flipped = flips
	r := portsmocks.NewMockRenderer(t)
	var mu sync.Mutex
	var fences []*os.File
	pipeBuf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{pipeBuf(), pipeBuf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		f, w, _ := os.Pipe()
		w.Close() // readable: signalled
		mu.Lock()
		fences = append(fences, f)
		mu.Unlock()
		return f, nil
	})
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	frameCommits := func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		n := 0
		for _, c := range *commits {
			if c.user == userFrame {
				n++
			}
		}
		return n
	}
	scenes := make(chan ports.Scene, 1)
	contents := make(chan ports.SurfaceContent, 1)
	presented := make(chan ports.OutputPresented, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, contents, nil, presented)
	}()
	scenes <- ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 10, H: 10}}}}
	waitFor(t, func() bool { return frameCommits() == 1 })
	// Frame 1 is in flight: content 3 replaces what it may be reading.
	contents <- ports.SurfaceContent{ID: 1, Seq: 3, SHM: &ports.SHMBuffer{}}
	select {
	case p := <-presented:
		t.Fatalf("report while a frame is in flight: %+v", p)
	case <-time.After(30 * time.Millisecond):
	}
	flips <- flipEvent{crtc: tCrtc, user: userFrame, when: time.Second, seq: 1}
	select {
	case p := <-presented:
		if p.Flip == nil || p.Flip.When != time.Second || p.Seen[1] != 3 {
			t.Fatalf("report %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no report after the flip")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fences) < 3 {
		t.Fatalf("%d renders", len(fences))
	}
	for _, f := range fences {
		if _, err := f.Stat(); err == nil {
			t.Fatal("frame fence left open")
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
