package drm

import (
	"bytes"
	"context"
	"errors"
	"image"
	"maps"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/presented"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
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

// commitRec is one recorded atomic commit.
type commitRec struct {
	req   atomicReq
	flags uint32
	user  uint64
	at    time.Time // when the commit was made
}

// testOutput returns an output on a mocked kms that records commits;
// commit returns the next error of errs (nil when empty).
func testOutput(t *testing.T, errs ...error) (*Output, *mockkms, *[]commitRec) {
	o, k, commits, _ := testOutputMu(t, errs...)
	return o, k, commits
}

// testOutputMu is testOutput with the mutex that guards the recorded
// commits (Run commits on its goroutine).
func testOutputMu(t *testing.T, errs ...error) (*Output, *mockkms, *[]commitRec, *sync.Mutex) {
	return testOutputRules(t, nil, errs...)
}

// testOutputRules is testOutputMu with an optional driver rule: a non-nil
// rule sees every commit (under the commit mutex) and may refuse it.
func testOutputRules(t *testing.T, rule func(*atomicReq, uint32) error, errs ...error) (*Output, *mockkms, *[]commitRec, *sync.Mutex) {
	k := newMockkms(t)
	var commits []commitRec
	commitMu := &sync.Mutex{}
	k.EXPECT().commit(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(r *atomicReq, flags uint32, user uint64) error {
		commitMu.Lock()
		defer commitMu.Unlock()
		c := commitRec{flags: flags, user: user, at: time.Now()}
		c.req.objs = append([]uint32(nil), r.objs...)
		for _, ps := range r.props {
			c.req.props = append(c.req.props, append([]propValue(nil), ps...))
		}
		commits = append(commits, c)
		if rule != nil {
			if err := rule(r, flags); err != nil {
				return err
			}
		}
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
	o := &Output{k: k, frame: frameLifecycle{serials: &atomic.Uint64{}}, crtc: tCrtc, conn: connector{id: tConn, name: "DP-1"}, mode: modeInfo{HDisplay: 200, VDisplay: 100, VRefresh: 60}, log: zerowrap.Default(), renderLog: zerowrap.Default(),
		crtcProps: map[string]uint32{"MODE_ID": pMode, "ACTIVE": pActive, "VRR_ENABLED": pVRR}, connCrtc: pConnCrtc, vrrProp: pVRR,
		primary: primary, cursor: cur, fbs: [2]uint32{70, 71}, clientFBs: map[uint64]*clientFB{}, scanout: true, tearing: true, asyncFence: true, planeRect: fullPlaneRect(200, 100), seatDisable: make(chan chan struct{})}
	return o, k, &commits, commitMu
}

// eventOf is the flip event of commit c.
func eventOf(c commitRec) flipEvent { return flipEvent{crtc: tCrtc, user: c.user} }

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
	if c.flags != atomicNonblock|flipEventFlag || c.user&3 != userFrame || c.user>>userKindBits != o.frame.pendingSerial {
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
	if !o.frame.pendingCommit() || !o.vrrOn || o.cursor.applied.x != 12 {
		t.Fatalf("state pending=%v vrr=%v cursor=%+v", o.frame.pendingCommit(), o.vrrOn, o.cursor.applied)
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
	if c.flags != atomicNonblock|flipEventFlag || c.user&3 != userState || !o.frame.pendingCommit() {
		t.Fatalf("cursor commit flags %#x user %d pending %v", c.flags, c.user, o.frame.pendingCommit())
	}
	// Its event is not a frame: nothing is reported.
	seen := map[ports.WindowID]uint64{}
	o.completed(eventOf(c), seen)
	if o.frame.pendingCommit() || o.reports.Len() != 0 || o.flips != 0 {
		t.Fatalf("pending=%v unsent=%d flips=%d", o.frame.pendingCommit(), o.reports.Len(), o.flips)
	}
	// Nothing changed: no commit.
	if err := o.commitState(false); err != nil || len(*commits) != 1 {
		t.Fatalf("idle commit: %v, %d commits", err, len(*commits))
	}
}

// Under VRR with a game shown, a cursor move rides on the next frame: a
// cursor-only commit would hold the panel for its slowest refresh. A game
// that stops drawing still gets the cursor after cursorMinInterval.
func TestVRRGameCursorWaitsForFrame(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.vrrOn, o.vrrGame = true, true
	o.lastFrame = time.Now()
	o.cursor.Move(5, 5)
	if err := o.commitState(o.stateVRR()); err != nil {
		t.Fatal(err)
	}
	if len(*commits) != 0 || !o.cursorHeld || o.frame.pendingCommit() {
		t.Fatalf("cursor committed alone: %d commits held=%v", len(*commits), o.cursorHeld)
	}
	// The next frame carries the cursor.
	if err := o.commitFrame(70, nil, false, true, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if v, _ := (*commits)[0].req.value(tCursor, 14); v != 5 || o.cursorHeld {
		t.Fatalf("frame cursor x %d held=%v", v, o.cursorHeld)
	}
	o.completed(eventOf((*commits)[0]), map[ports.WindowID]uint64{})
	// The game stalls: the cursor commits alone after the interval.
	o.lastFrame = time.Now().Add(-cursorMinInterval)
	o.cursor.Move(8, 8)
	if err := o.commitState(o.stateVRR()); err != nil {
		t.Fatal(err)
	}
	if len(*commits) != 2 || o.cursorHeld {
		t.Fatalf("stalled game: %d commits held=%v", len(*commits), o.cursorHeld)
	}
	if v, _ := (*commits)[1].req.value(tCursor, 14); v != 8 {
		t.Fatalf("cursor x %d", v)
	}
}

// A held move reversed before the next frame holds nothing: the cursor
// timer is not armed again.
func TestHeldCursorMovedBackClearsHold(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.vrrOn, o.vrrGame, o.lastFrame = true, true, time.Now()
	o.cursor.Move(0, 0)
	o.cursor.applied = o.cursor.desired() // on screen at (0, 0)
	o.cursor.Move(5, 5)
	if err := o.commitState(o.stateVRR()); err != nil || !o.cursorHeld {
		t.Fatalf("err %v held=%v", err, o.cursorHeld)
	}
	o.cursor.Move(0, 0)
	if err := o.commitState(o.stateVRR()); err != nil || o.cursorHeld || len(*commits) != 0 {
		t.Fatalf("err %v held=%v commits %d", err, o.cursorHeld, len(*commits))
	}
}

// Without a game, the cursor keeps its own commits.
func TestCursorCommitsAloneWithoutGame(t *testing.T) {
	o, _, commits := testOutput(t)
	o.cursor.image = true
	o.vrrOn, o.lastFrame = true, time.Now()
	o.composedSince = time.Now()
	o.cursor.Move(5, 5)
	if err := o.commitState(o.stateVRR()); err != nil || len(*commits) != 1 || o.cursorHeld {
		t.Fatalf("err %v, %d commits held=%v", err, len(*commits), o.cursorHeld)
	}
}

func TestBusyCommitStaysPending(t *testing.T) {
	o, _, _ := testOutput(t, unix.EBUSY)
	err := o.commitFrame(70, nil, false, false, pendingFrame{})
	enabled := true
	if !o.commitFailed(err, &enabled) || !o.frame.pendingCommit() || !enabled {
		t.Fatalf("EBUSY: pending=%v enabled=%v", o.frame.pendingCommit(), enabled)
	}
}

func TestFlipReportsTimestampAndShownFrame(t *testing.T) {
	o, _, commits := testOutput(t)
	o.vrrOn = false
	o.mode.Clock, o.mode.HTotal, o.mode.VTotal = 0, 0, 0
	if err := o.commitFrame(70, nil, false, false, pendingFrame{shows: map[ports.WindowID]uint64{1: 4}}); err != nil {
		t.Fatal(err)
	}
	o.completed(flipEvent{user: (*commits)[0].user, when: 5 * time.Second, seq: 11}, map[ports.WindowID]uint64{1: 4})
	if o.reports.Len() != 1 {
		t.Fatalf("reports %d", o.reports.Len())
	}
	f := o.reports.Unsent()[0].Flip
	if f == nil || f.When != 5*time.Second || f.Seq != 11 || f.Refresh != time.Second/60 || f.Shows[1] != 4 || !f.HardwareClock {
		t.Fatalf("flip %+v", f)
	}
}

// Unchanged reports share an immutable snapshot; a later content update
// must not change an earlier report, even after it has been sent.
func TestReportSeenAllocations(t *testing.T) {
	o, _, _ := testOutput(t)
	seen := map[ports.WindowID]uint64{1: 1, 2: 2}
	o.report(nil, seen)
	// Keep the queue bounded without measuring the queue's growth.
	if allocs := testing.AllocsPerRun(100, func() { o.report(nil, seen) }); allocs != 0 {
		t.Errorf("unchanged report: %.1f allocs, want 0", allocs)
	} else {
		t.Logf("unchanged report: %.1f allocs", allocs)
	}
	previous := o.reports.Unsent()[0].Seen
	seen[1] = 3
	o.report(nil, seen)
	if previous[1] != 1 || o.reports.Unsent()[0].Seen[1] != 3 {
		t.Fatalf("snapshot mutated: old %v new %v", previous, o.reports.Unsent()[0].Seen)
	}
}

func TestShownBySnapshotAllocations(t *testing.T) {
	o := &Output{}
	s := ports.Scene{OutputWidth: 10, OutputHeight: 10, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 10, H: 10}}}}
	seen := map[ports.WindowID]uint64{1: 1, 2: 2}
	previous := o.shownBy(s, seen)
	if allocs := testing.AllocsPerRun(100, func() { o.shownBy(s, seen) }); allocs != 0 {
		t.Errorf("unchanged shows: %.1f allocs, want 0", allocs)
	}
	seen[1] = 3
	current := o.shownBy(s, seen)
	if previous[1] != 1 || current[1] != 3 {
		t.Fatalf("snapshot mutated: old %v new %v", previous, current)
	}
	direct := o.directShownBy(1, 3)
	if allocs := testing.AllocsPerRun(100, func() { o.directShownBy(1, 3) }); allocs != 0 {
		t.Errorf("unchanged direct shows: %.1f allocs, want 0", allocs)
	}
	o.directShownBy(1, 4)
	if direct[1] != 3 {
		t.Fatalf("direct snapshot mutated: %v", direct)
	}
}

func TestReportsKeepEveryFlip(t *testing.T) {
	o, _, _ := testOutput(t)
	seen := map[ports.WindowID]uint64{1: 1}
	o.report(&ports.FlipInfo{Seq: 1}, seen)
	o.report(nil, seen)
	o.report(&ports.FlipInfo{Seq: 2}, seen)
	o.report(nil, map[ports.WindowID]uint64{1: 3})
	if o.reports.Len() != 2 || o.reports.Unsent()[0].Flip.Seq != 1 || o.reports.Unsent()[1].Flip.Seq != 2 || o.reports.Unsent()[1].Seen[1] != 3 {
		t.Fatalf("unsent %+v", o.reports.Unsent())
	}
	for i := range presented.MaxUnsent + 3 {
		o.report(&ports.FlipInfo{Seq: uint64(10 + i), Shows: map[ports.WindowID]uint64{}}, seen)
	}
	if o.reports.Len() != presented.MaxUnsent || o.reports.Unsent()[0].Flip.Merged == 0 {
		t.Fatalf("cap: %d reports, first merged %d", o.reports.Len(), o.reports.Unsent()[0].Flip.Merged)
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

// A composed fullscreen frame keeps VRR and restarts the hold; state
// commits (cursor, vrrOff timer) keep it too until the fullscreen ends.
func TestVRRKeptWhileFullscreenComposed(t *testing.T) {
	o, _, _ := testOutput(t)
	o.vrrOn = true
	o.composedSince = time.Now().Add(-time.Second)
	if !o.wantVRR(true) || !o.composedSince.IsZero() {
		t.Fatal("composed fullscreen dropped VRR")
	}
	// Cursor-only state commit between frames.
	if !o.stateVRR() {
		t.Fatal("state commit dropped VRR during fullscreen")
	}
	// Fullscreen ends, a tiled window stays on the overlay plane: the
	// hold starts, then VRR goes off.
	o.shown = 9
	if !o.wantVRR(false) || o.composedSince.IsZero() {
		t.Fatal("hold not started on exit")
	}
	o.composedSince = time.Now().Add(-time.Second)
	if o.stateVRR() {
		t.Fatal("VRR kept after the hold (overlay buffer taken for a game)")
	}
}

// A fullscreen game on a workspace that is left keeps VRR only for the
// hold: its hidden window no longer counts as a game frame, and its
// commits do not draw frames.
func TestVRRHiddenGameOnlyHolds(t *testing.T) {
	o, _, _ := testOutput(t)
	game := ports.SceneWindow{ID: 1, Rect: ports.Rect{W: 100, H: 50}, Fullscreen: true}
	shown := ports.Scene{OutputWidth: 100, OutputHeight: 50, Windows: []ports.SceneWindow{game}}
	o.vrrOn = true
	if !o.wantVRR(fullscreenShown(&shown)) {
		t.Fatal("VRR off while the game is shown")
	}
	game.Hidden, game.Rect = true, ports.Rect{}
	left := ports.Scene{OutputWidth: 100, OutputHeight: 50, Windows: []ports.SceneWindow{game, {ID: 2, Rect: ports.Rect{W: 100, H: 50}}}}
	if fullscreenShown(&left) || left.Shows(1) {
		t.Fatal("hidden game still drives frames")
	}
	if !o.wantVRR(fullscreenShown(&left)) {
		t.Fatal("VRR dropped before the hold")
	}
	o.composedSince = time.Now().Add(-vrrHold - time.Millisecond)
	if o.stateVRR() {
		t.Fatal("VRR kept after the hold for a hidden game")
	}
}

// A VRR refusal on a composed frame with an overlay retries with the
// overlay: the composed image left its window out.
func TestVRRRefusedKeepsOverlay(t *testing.T) {
	o, k, commits := overlayOutput(t, unix.EINVAL)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	ov, _ := o.overlayFrame(s, c)
	if ov.fb == 0 {
		t.Fatal("no overlay")
	}
	if err := o.commitWith(70, nil, false, true, pendingFrame{queued: ov.buf}, ov); err != nil {
		t.Fatal(err)
	}
	if len(*commits) != 2 || o.vrrProp != 0 {
		t.Fatalf("%d commits, vrrProp %d", len(*commits), o.vrrProp)
	}
	if v, _ := (*commits)[1].req.value(tOverlay, pFB); v != 88 || o.overlayOn != ov.buf {
		t.Fatalf("retry overlay fb %d, overlayOn %d", v, o.overlayOn)
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
	if len(*commits) != 2 || o.vrrProp != 0 || !o.frame.pendingCommit() {
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
	o, k, commits, commitMu := testOutputMu(t)
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
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{pipeBuf(), pipeBuf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		f, w, _ := os.Pipe()
		_, _ = w.Write([]byte{1}) // readable: signalled
		w.Close()
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
			if c.user&3 == userFrame {
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
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, contents, nil, presented, nil, nil)
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
	commitMu.Lock()
	last := (*commits)[len(*commits)-1]
	commitMu.Unlock()
	flips <- flipEvent{crtc: tCrtc, user: last.user, when: time.Second, seq: 1}
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

// Run: a fullscreen window composed under an overlay layer keeps VRR on in
// the frame commit; a tiled scene turns it off only after vrrHold.
func TestRunComposedFullscreenKeepsVRR(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.cursor, o.tearing = nil, false
	flips := make(chan flipEvent, 1)
	o.flipped = flips
	r := portsmocks.NewMockRenderer(t)
	pipeBuf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{pipeBuf(), pipeBuf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	lastFrame := func() (commitRec, int) {
		commitMu.Lock()
		defer commitMu.Unlock()
		n, last := 0, commitRec{}
		for _, c := range *commits {
			if c.user&3 == userFrame {
				n, last = n+1, c
			}
		}
		return last, n
	}
	scenes := make(chan ports.Scene, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, nil, nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	scenes <- ports.Scene{OutputWidth: 200, OutputHeight: 100,
		Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 200, H: 100}, Fullscreen: true}},
		Layers:  []ports.SceneLayer{{ID: 5, Layer: ports.LayerOverlay, Rect: ports.Rect{W: 10, H: 10}}}}
	waitFor(t, func() bool { _, n := lastFrame(); return n == 1 })
	last, _ := lastFrame()
	if v, ok := last.req.value(tCrtc, pVRR); !ok || v != 1 {
		t.Fatalf("composed fullscreen frame VRR %d %v", v, ok)
	}
	flips <- flipEvent{crtc: tCrtc, user: last.user, when: time.Second, seq: 1}
	scenes <- ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 100, H: 100}}}}
	waitFor(t, func() bool { _, n := lastFrame(); return n == 2 })
	last, _ = lastFrame()
	if v, _ := last.req.value(tCrtc, pVRR); v != 1 {
		t.Fatal("VRR dropped before vrrHold")
	}
	flips <- flipEvent{crtc: tCrtc, user: last.user, when: 2 * time.Second, seq: 2}
	// The vrrOff timer turns VRR off with a state commit after the hold.
	waitFor(t, func() bool {
		commitMu.Lock()
		defer commitMu.Unlock()
		c := (*commits)[len(*commits)-1]
		v, ok := c.req.value(tCrtc, pVRR)
		return c.user&3 == userState && ok && v == 0
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
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

// The stats tick lets the renderer free what the output no longer draws,
// even while nothing renders; a failed trim stops the output.
func TestRunTrimsRendererOnStatsTick(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor, o.tearing, o.fbs = nil, false, [2]uint32{}
	ticks := make(chan time.Time)
	clk := portsmocks.NewMockClock(t)
	tk := portsmocks.NewMockTicker(t)
	clk.EXPECT().NewTicker(10 * time.Second).Return(tk).Once()
	tk.EXPECT().C().Return(ticks)
	tk.EXPECT().Stop().Return().Once()
	now := time.Unix(1000, 0)
	clk.EXPECT().Now().Return(now)
	o.clock = clk
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().Close().Return().Once()
	r.EXPECT().TakeRedrawn().Return(1234).Once()
	var statsLog bytes.Buffer // written by Run's goroutine, read after it ends
	o.log = zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &statsLog})
	o.flipStats = flipStats{missedVblanks: 3, maxInterval: 50 * time.Millisecond, maxFlipToRead: 25 * time.Millisecond, lateFences: 2}
	trimmed := make(chan struct{}, 2)
	failure := os.ErrInvalid
	calls := 0 // Run's goroutine only
	r.EXPECT().Trim(now).RunAndReturn(func(time.Time) error {
		trimmed <- struct{}{}
		if calls++; calls == 2 {
			return failure
		}
		return nil
	}).Twice()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	// Switched away from the start: no target, no frame.
	active := make(chan bool, 1)
	active <- false
	done := make(chan error, 1)
	go func() {
		done <- o.Run(context.Background(), func(int, int) (ports.Renderer, error) { return r, nil }, nil, active, nil, nil, nil, make(chan ports.OutputPresented, 1), nil, nil)
	}()
	ticks <- now
	<-trimmed
	ticks <- now
	if err := <-done; !errors.Is(err, failure) {
		t.Fatalf("run: %v", err)
	}
	// Reported once, then reset. late_fences needs flip tracing.
	for _, want := range []string{`"message":"stats"`, `"missed_vblanks":3`, `"max_flip_interval_ms":50`, `"max_flip_to_read_ms":25`, `"redrawn_pixels":1234`} {
		if !strings.Contains(statsLog.String(), want) {
			t.Fatalf("stats entry lacks %s: %s", want, statsLog.String())
		}
	}
	if strings.Contains(statsLog.String(), "late_fences") || o.flipStats != (flipStats{}) {
		t.Fatalf("late fences untraced or stats not reset: %s %+v", statsLog.String(), o.flipStats)
	}
}

// A render failure must complete captures queued before the first frame.
func TestCaptureRenderFailureClosesPendingRequest(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor, o.tearing = nil, false
	r := portsmocks.NewMockRenderer(t)
	boom := errors.New("render failed")
	r.EXPECT().SetHDR(float64(0)).Return().Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	makeBuf := func() ports.DMABuf {
		f, err := os.CreateTemp(t.TempDir(), "target")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{makeBuf(), makeBuf()}, nil).Once()
	r.EXPECT().Render(ports.Scene{Background: "#000000"}, mock.Anything).Return(nil, nil).Twice()
	r.EXPECT().Render(mock.MatchedBy(func(s ports.Scene) bool { return s.Background != "#000000" }), mock.Anything).Return(nil, boom).Once()
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scenes := make(chan ports.Scene, 1)
	requests := make(chan ports.CaptureRequest)
	replies := make(chan ports.CaptureDone, 1)
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, nil, scenes, nil, nil, nil, requests, replies)
	}()
	// The unbuffered handoff proves Run owns the request before any scene.
	select {
	case requests <- ports.CaptureRequest{ID: 9, Dst: ports.SHMBuffer{File: f}}:
	case <-time.After(2 * time.Second):
		t.Fatal("output did not accept capture")
	}
	scenes <- ports.Scene{Seq: 1, Scale: 1}
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("run error %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("output did not stop after render failure")
	}
	select {
	case result := <-replies:
		if result.ID != 9 || result.Err == nil {
			t.Fatalf("capture result %+v", result)
		}
	default:
		t.Fatal("render failure lost capture completion")
	}
	if _, err := f.Stat(); err == nil {
		t.Fatal("render failure leaked capture descriptor")
	}
}

// A request must render even when the output already has an unchanged scene.
func TestCaptureForcesDRMComposition(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.cursor, o.tearing = nil, false
	flips := make(chan flipEvent, 2)
	o.flipped = flips
	r := &portsmocks.MockRenderer{}
	makeBuf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe() // SDR output
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{makeBuf(), makeBuf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	rendered := make(chan ports.Scene, 3)
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		rendered <- s
		return nil, nil
	})
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil)
	frame.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.MatchedBy(func(p []byte) bool { return len(p) >= 16 }), 8).RunAndReturn(func(_ image.Rectangle, dst []byte, _ int) error { copy(dst, []byte{3, 4, 5, 255}); return nil }).Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	scenes := make(chan ports.Scene, 1)
	requests := make(chan ports.CaptureRequest, 1)
	replies := make(chan ports.CaptureDone, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, nil, scenes, nil, nil, make(chan ports.OutputPresented, 8), requests, replies)
	}()
	scenes <- ports.Scene{OutputWidth: 200, OutputHeight: 100, Background: "#000000"}
	select {
	case <-rendered:
	case <-time.After(2 * time.Second):
		t.Fatal("no initial render")
	}
	waitFor(t, func() bool { commitMu.Lock(); defer commitMu.Unlock(); return len(*commits) > 0 })
	commitMu.Lock()
	last := (*commits)[len(*commits)-1]
	commitMu.Unlock()
	flips <- flipEvent{crtc: tCrtc, user: last.user, when: time.Second, seq: 1}
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(16); err != nil {
		t.Fatal(err)
	}
	requests <- ports.CaptureRequest{ID: 4, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Dst: ports.SHMBuffer{File: f}}
	select {
	case result := <-replies:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("capture not replied")
	}
	select {
	case <-rendered:
	case <-time.After(2 * time.Second):
		t.Fatal("capture did not render")
	}
	frame.Calls = nil // Testify must not inspect unmapped slice during expectation cleanup.
	if _, err := f.Stat(); err == nil {
		t.Fatal("fd not closed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunPanicReportsReadinessError(t *testing.T) {
	o, _, _ := testOutput(t)
	o.ready = make(chan error, 1)
	defer func() {
		if recover() == nil {
			t.Fatal("panic not propagated")
		}
		select {
		case err := <-o.Ready():
			if err == nil {
				t.Fatal("panic reported as ready")
			}
		default:
			t.Fatal("missing readiness")
		}
	}()
	o.Run(context.Background(), func(int, int) (ports.Renderer, error) { panic("renderer panic") }, nil, nil, nil, nil, nil, nil, nil, nil)
}

// stampRun starts Run on an output with unbuffered scene and content
// channels: a send completes once Run took the message, and the next send
// once it went round its loop again, which is the sync point for what the
// loop did with the first.
type stampRun struct {
	o           *Output
	scenes      chan ports.Scene
	contents    chan ports.SurfaceContent
	flips       chan flipEvent
	wake        chan ports.SecurityState
	state       *atomic.Uint64
	frameCommit func() int
	lastCommit  func() commitRec
	stop        func()
}

func startStampRun(t *testing.T) *stampRun {
	o, k, commits, commitMu := testOutputMu(t)
	o.cursor, o.tearing = nil, false
	sr := &stampRun{o: o, scenes: make(chan ports.Scene), contents: make(chan ports.SurfaceContent), flips: make(chan flipEvent, 1), wake: make(chan ports.SecurityState, 1), state: &atomic.Uint64{}}
	o.flipped = sr.flips
	securityGate(t, o, sr.state)
	o.SecurityChanges = sr.wake
	r := portsmocks.NewMockRenderer(t)
	pipeBuf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{pipeBuf(), pipeBuf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	sr.frameCommit = func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		n := 0
		for _, c := range *commits {
			if c.user&3 == userFrame {
				n++
			}
		}
		return n
	}
	sr.lastCommit = func() commitRec {
		commitMu.Lock()
		defer commitMu.Unlock()
		return (*commits)[len(*commits)-1]
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), sr.scenes, sr.contents, nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	// stop ends Run: its state is ours to read afterwards.
	sr.stop = func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	return sr
}

// Run: a frame wanted while the previous one is in flight is stamped when
// it becomes dirty, and the commit that follows takes the stamp.
func TestRunStampsWhenFrameWasWanted(t *testing.T) {
	sr := startStampRun(t)
	scene := ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 10, H: 10}}}}
	sr.scenes <- scene
	waitFor(t, func() bool { return sr.frameCommit() == 1 })
	// Frame 1 is in flight: a new scene arrives and waits.
	last := sr.lastCommit()
	scene.Seq = 2
	before := monotonic()
	sr.scenes <- scene
	sr.contents <- ports.SurfaceContent{ID: 1, Seq: 1, SHM: &ports.SHMBuffer{}} // Run went round its loop
	flipSent := monotonic()
	sr.flips <- flipEvent{crtc: tCrtc, user: last.user, when: time.Second, seq: 1}
	waitFor(t, func() bool { return sr.frameCommit() == 2 })
	sr.stop()
	if sr.o.wantedAt != 0 {
		t.Fatalf("stamp left after the commit: %s", sr.o.wantedAt)
	}
	f := sr.o.frame.pendingFrame
	if !f.frame || f.wantedAt < before || f.wantedAt >= flipSent {
		t.Fatalf("second frame (%v) wanted at %s, scene sent at %s, flip sent at %s", f.frame, f.wantedAt, before, flipSent)
	}
}

// A change of security state drops the scene, and with it the stamp of
// the frame it wanted.
func TestRunSecurityResetClearsWantedAt(t *testing.T) {
	sr := startStampRun(t)
	scene := ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 10, H: 10}}}}
	sr.scenes <- scene
	waitFor(t, func() bool { return sr.frameCommit() == 1 })
	scene.Seq = 2
	sr.scenes <- scene
	sr.contents <- ports.SurfaceContent{ID: 1, Seq: 1, SHM: &ports.SHMBuffer{}} // stamped by now
	// A new security generation, not protected.
	sr.state.Store(2)
	sr.wake <- ports.SecurityState{}
	sr.contents <- ports.SurfaceContent{ID: 1, Seq: 2, SHM: &ports.SHMBuffer{}} // Run went round and reset
	sr.stop()
	if sr.o.wantedAt != 0 {
		t.Fatalf("stamp kept across the security reset: %s", sr.o.wantedAt)
	}
}

// A scene whose Seq the output already holds draws nothing new: no frame,
// until a scene with a new Seq arrives.
func TestOutputSameSeqRendersOnce(t *testing.T) {
	sr := startStampRun(t)
	scene := ports.Scene{Seq: 5, OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 10, H: 10}}}}
	sr.scenes <- scene
	waitFor(t, func() bool { return sr.frameCommit() == 1 })
	sr.flips <- flipEvent{crtc: tCrtc, user: sr.lastCommit().user, when: time.Second, seq: 1}
	sr.scenes <- scene
	// Unshown content only sends Run round its loop: the flip and the held
	// scene are handled by the time the second one is taken.
	sr.contents <- ports.SurfaceContent{ID: 99, Seq: 1, SHM: &ports.SHMBuffer{}}
	sr.contents <- ports.SurfaceContent{ID: 99, Seq: 2, SHM: &ports.SHMBuffer{}}
	if n := sr.frameCommit(); n != 1 {
		t.Fatalf("%d frames after a scene with the held Seq", n)
	}
	scene.Seq = 6
	sr.scenes <- scene
	waitFor(t, func() bool { return sr.frameCommit() == 2 })
	sr.stop()
}

// Run: the empty content of a window that unmapped while the scene still
// draws it (its close animation) keeps the last content for the renderer,
// without its Acquire fence; a scene that no longer lists the window drops
// it.
func TestRunKeepsLastContentOfLeavingWindow(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor, o.tearing = nil, false
	r := portsmocks.NewMockRenderer(t)
	pipeBuf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	var mu sync.Mutex
	var last map[ports.WindowID]ports.SurfaceContent
	var lastSeq uint64
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{pipeBuf(), pipeBuf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, c map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		mu.Lock()
		last, lastSeq = maps.Clone(c), s.Seq
		mu.Unlock()
		f, w, _ := os.Pipe()
		_, _ = w.Write([]byte{1})
		w.Close()
		return f, nil
	})
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	// drawn waits for a frame of scene seq and returns what it drew for
	// window 1.
	drawn := func(seq uint64, cond func(ports.SurfaceContent, bool) bool) (ports.SurfaceContent, bool) {
		t.Helper()
		var c ports.SurfaceContent
		var ok bool
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			c, ok = last[1]
			return lastSeq == seq && cond(c, ok)
		})
		return c, ok
	}
	scenes := make(chan ports.Scene, 1)
	contents := make(chan ports.SurfaceContent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, make(chan bool), scenes, contents, nil, make(chan ports.OutputPresented, 64), nil, nil)
	}()
	acq, acqW, _ := os.Pipe()
	defer acq.Close()
	defer acqW.Close()
	win := ports.SceneWindow{ID: 1, Rect: ports.Rect{W: 10, H: 10}}
	scenes <- ports.Scene{Seq: 1, OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{win}}
	full := ports.SurfaceContent{ID: 1, Seq: 1, Width: 10, Height: 10, DMABuf: &ports.DMABuf{ID: 5, Planes: []ports.DMABufPlane{{}}}, Acquire: acq}
	contents <- full
	drawn(1, func(c ports.SurfaceContent, ok bool) bool { return ok && c.Acquire == acq })
	// The window unmaps: its empty content arrives before the scene that
	// draws it leaving (fading). The last content stays, fence dropped.
	contents <- ports.SurfaceContent{ID: 1, Seq: 2}
	kept, _ := drawn(1, func(c ports.SurfaceContent, ok bool) bool { return !ok || c.Acquire == nil })
	if kept.DMABuf != full.DMABuf || kept.Seq != 1 {
		t.Fatalf("frame after the empty content drew %+v, want the last content", kept)
	}
	// A leaving frame: still drawn from the kept content.
	win.Fade = 0.5
	scenes <- ports.Scene{Seq: 2, OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{win}}
	if c, ok := drawn(2, func(ports.SurfaceContent, bool) bool { return true }); !ok || c.DMABuf != full.DMABuf {
		t.Fatalf("the leaving frame drew %+v (ok %t), want the kept content", c, ok)
	}
	// The scene no longer lists it: gone.
	scenes <- ports.Scene{Seq: 3, OutputWidth: 200, OutputHeight: 100}
	if c, ok := drawn(3, func(ports.SurfaceContent, bool) bool { return true }); ok {
		t.Fatalf("the kept content %+v survived a scene without the window", c)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
