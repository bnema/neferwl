package wayland

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/presentationtime"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// presentedEvent is one feedback answer: presented (with its fields) or
// discarded.
type presentedEvent struct {
	discarded   bool
	when        time.Duration
	refresh     uint32
	seq         uint64
	flags       uint32
	syncOutputs int
}

type feedbackEvents struct {
	wlturbo.BaseProxy
	out  chan presentedEvent
	sync int
}

func (p *feedbackEvents) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case presentationtime.WpPresentationFeedbackEventSyncOutput:
		p.sync++
	case presentationtime.WpPresentationFeedbackEventPresented:
		d := e.Data()
		u := func(i int) uint32 { return binary.NativeEndian.Uint32(d[i*4:]) }
		sec := uint64(u(0))<<32 | uint64(u(1))
		p.out <- presentedEvent{when: time.Duration(sec)*time.Second + time.Duration(u(2)), refresh: u(3), seq: uint64(u(4))<<32 | uint64(u(5)), flags: u(6), syncOutputs: p.sync}
	case presentationtime.WpPresentationFeedbackEventDiscarded:
		p.out <- presentedEvent{discarded: true}
	}
}

// presentServer is a server whose flips the test sends.
func presentServer(t *testing.T) (*Server, chan ports.ClientEvent, chan ports.ClientCommand, chan ports.SurfaceContent, chan ports.OutputPresented, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 16)
	contents := make(chan ports.SurfaceContent, 64)
	presented := make(chan ports.OutputPresented, 16)
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs}, Channels{Events: events, Commands: commands, Contents: contents, Presented: presented}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, events, commands, contents, presented, dir
}

func TestPresentationFeedback(t *testing.T) {
	s, events, commands, contents, presented, dir := presentServer(t)
	c := protocolClient(t, s, dir)
	w, surf, xdg := surfaceMapper(t, c, events)()
	registerProtocol(t, c, xdg)
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 100, Height: 100, Output: "HEADLESS-1", Visible: true}
	for deadline := time.Now().Add(2 * time.Second); ; {
		var out string
		s.display.Do(func() { out = s.frameOutput(s.windows[w.ID].xdg.surface) })
		if out != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("configure not applied")
		}
		time.Sleep(2 * time.Millisecond)
	}
	registerProtocol(t, c, bindProtocol(t, c, "wl_output"))
	pres := bindProtocol(t, c, "wp_presentation")
	registerProtocol(t, c, pres)
	// Drain contents so seqs are known.
	lastSeq := func() uint64 {
		t.Helper()
		var seq uint64
		deadline := time.After(2 * time.Second)
		for {
			select {
			case got := <-contents:
				if got.ID == w.ID {
					seq = got.Seq
				}
			case <-time.After(50 * time.Millisecond):
				if seq != 0 {
					return seq
				}
			case <-deadline:
				t.Fatal("no content")
			}
		}
	}
	feedback := func() *feedbackEvents {
		t.Helper()
		fb := c.AllocateID()
		p := &feedbackEvents{out: make(chan presentedEvent, 4)}
		p.SetID(fb)
		registerWireProxy(c, p)
		requestProtocol(t, c, pres, presentationtime.WpPresentationRequestFeedback, surf, fb)
		return p
	}
	answer := func(p *feedbackEvents, what string) presentedEvent {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-p.out:
				return ev
			case <-time.After(5 * time.Millisecond):
			}
		}
		t.Fatalf("no answer: %s", what)
		return presentedEvent{}
	}
	// A fresh 1×1 buffer per commit: every commit is a new content.
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("present", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 4); err != nil {
		t.Fatal(err)
	}
	pool := c.AllocateID()
	registerProtocol(t, c, pool)
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, int32(4)); err != nil {
		t.Fatal(err)
	}
	commit := func() {
		buf := c.AllocateID()
		registerProtocol(t, c, buf)
		requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buf, int32(0), int32(1), int32(1), int32(4), uint32(0))
		requestProtocol(t, c, surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
		requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
		if err := c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	flip := func(when time.Duration, seq uint64, shows uint64, zero ports.WindowID) {
		presented <- ports.OutputPresented{Output: "HEADLESS-1", Flip: &ports.FlipInfo{When: when, Seq: seq, Refresh: time.Second / 60, ZeroCopy: zero, HardwareClock: true, Shows: map[ports.WindowID]uint64{w.ID: shows}}}
	}
	// With no flip, hiding must discard a waiting feedback immediately.
	hidden := feedback()
	commit()
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 100, Height: 100, Output: "HEADLESS-1", Visible: false}
	if ev := answer(hidden, "hidden without flip"); !ev.discarded {
		t.Fatalf("hidden feedback: %+v", ev)
	}
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 100, Height: 100, Output: "HEADLESS-1", Visible: true}
	// Wait for the resume before committing the next feedback: the command
	// goroutine applies it in its own display round trip, which an empty Do
	// here may overtake.
	for deadline := time.Now().Add(2 * time.Second); ; {
		var visible bool
		s.display.Do(func() { visible = s.windows[w.ID].last.Visible })
		if visible {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resume not applied")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Presented with the flip's time, counter and flags.
	a := feedback()
	commit()
	seqA := lastSeq()
	flip(5*time.Second+7, 42, seqA, w.ID)
	ev := answer(a, "presented")
	if ev.discarded || ev.when != 5*time.Second+7 || ev.seq != 42 || ev.refresh != uint32(time.Second/60) || ev.syncOutputs != 1 {
		t.Fatalf("presented %+v", ev)
	}
	want := uint32(presentationtime.WpPresentationFeedbackKindVsync | presentationtime.WpPresentationFeedbackKindHwClock | presentationtime.WpPresentationFeedbackKindHwCompletion | presentationtime.WpPresentationFeedbackKindZeroCopy)
	if ev.flags != want {
		t.Fatalf("flags %#x want %#x", ev.flags, want)
	}
	// Two flips in one drain: two presented events, each with its time.
	b := feedback()
	commit()
	seqB := lastSeq()
	d := feedback()
	commit()
	seqD := lastSeq()
	flip(6*time.Second, 43, seqB, w.ID+1) // another window on the overlay
	flip(7*time.Second, 44, seqD, 0)
	if ev := answer(b, "first of two"); ev.when != 6*time.Second || ev.seq != 43 || ev.flags&uint32(presentationtime.WpPresentationFeedbackKindZeroCopy) != 0 {
		t.Fatalf("first %+v", ev)
	}
	if ev := answer(d, "second of two"); ev.when != 7*time.Second || ev.seq != 44 {
		t.Fatalf("second %+v", ev)
	}
	// Replaced before shown: discarded when the flip shows the newer one.
	old := feedback()
	commit()
	lastSeq()
	commit()
	seqNew := lastSeq()
	flip(8*time.Second, 45, seqNew, 0)
	if ev := answer(old, "replaced"); !ev.discarded {
		t.Fatalf("replaced: %+v", ev)
	}
	// A subsurface's feedback follows its root: presented with the root's
	// content that the flip shows.
	comp := bindProtocol(t, c, "wl_compositor")
	sub := bindProtocol(t, c, "wl_subcompositor")
	child, subsurf := c.AllocateID(), c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	requestProtocol(t, c, sub, wayland.SubcompositorRequestGetSubsurface, subsurf, child, surf)
	registerProtocol(t, c, subsurf)
	cfb := c.AllocateID()
	cp := &feedbackEvents{out: make(chan presentedEvent, 1)}
	cp.SetID(cfb)
	registerWireProxy(c, cp)
	requestProtocol(t, c, pres, presentationtime.WpPresentationRequestFeedback, child, cfb)
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	commit() // the parent applies the subsurface
	seqC := lastSeq()
	flip(8500*time.Millisecond, 50, seqC, 0)
	if ev := answer(cp, "subsurface"); ev.discarded || ev.seq != 50 {
		t.Fatalf("subsurface %+v", ev)
	}
	// Surface destroyed: discarded.
	gone := feedback()
	commit()
	lastSeq()
	requestProtocol(t, c, surf, wayland.SurfaceRequestDestroy)
	flip(9*time.Second, 46, 0, 0)
	if ev := answer(gone, "destroyed"); !ev.discarded {
		t.Fatalf("destroyed: %+v", ev)
	}
}

// A hidden window (no output) gets discarded at once.
func TestPresentationHiddenDiscarded(t *testing.T) {
	s, events, _, _, _, dir := presentServer(t)
	c := protocolClient(t, s, dir)
	_, surf, _ := surfaceMapper(t, c, events)()
	pres := bindProtocol(t, c, "wp_presentation")
	registerProtocol(t, c, pres)
	fb := c.AllocateID()
	p := &feedbackEvents{out: make(chan presentedEvent, 1)}
	p.SetID(fb)
	registerWireProxy(c, p)
	requestProtocol(t, c, pres, presentationtime.WpPresentationRequestFeedback, surf, fb)
	requestProtocol(t, c, surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-p.out:
		if !ev.discarded {
			t.Fatalf("%+v", ev)
		}
	default:
		t.Fatal("not discarded")
	}
}

// Commit timing: a commit targeted two refreshes ahead of the last flip
// is not applied on the next one.
func TestNextRefreshFollowsFlips(t *testing.T) {
	s := &Server{flips: map[string]ports.FlipInfo{}}
	s.outputs = []*output{{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "A", RefreshMilli: 60000}}}}
	now := time.Now()
	mono := monotonic(now)
	period := time.Second / 60
	s.flips["A"] = ports.FlipInfo{When: mono - period/4, Refresh: period}
	next := s.nextRefresh("A", now)
	if d := next.Sub(now); d < period*3/4-time.Millisecond || d > period*3/4+time.Millisecond {
		t.Fatalf("next refresh in %v, want %v", d, period*3/4)
	}
}

// Software flips (headless) answer feedback without flags and do not pace
// frame callbacks.
func TestSoftwareFlipsDoNotPace(t *testing.T) {
	if paces(ports.OutputPresented{Flip: &ports.FlipInfo{}}) {
		t.Fatal("software flip paces callbacks")
	}
	if !paces(ports.OutputPresented{Flip: &ports.FlipInfo{HardwareClock: true}}) {
		t.Fatal("display flip does not pace")
	}
}
