package core_test

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

type dragRig struct {
	client   chan ports.ClientEvent
	input    chan ports.InputEvent
	output   chan ports.OutputEvent
	commands chan ports.ClientCommand
	scenes   chan []ports.Scene
	last     []ports.Scene
	epoch    *atomic.Value
	// With a clock the test moves (animated rigs), frames carries the page
	// flips and outputs lists the plugged ones: settle lands every spring
	// before it reads the scene.
	frames  chan ports.OutputFrame
	clock   *stepClock
	outputs []string
	// lands counts the settles that found a spring running.
	lands int
}

// startDrag runs core with Super as Cmd, no border, 10 px gaps, and
// the given outputs. edit may change the config.
func startDragRig(t *testing.T, edit func(*ports.Config), outs ...ports.OutputInfo) *dragRig {
	t.Helper()
	return newDragRig(t, false, edit, outs...)
}

// startAnimatedDragRig is startDragRig with animations on and a clock the
// test moves: settle moves it past every spring and sends the page flips,
// so the scene it returns is the settled one, deterministically.
func startAnimatedDragRig(t *testing.T, edit func(*ports.Config), outs ...ports.OutputInfo) *dragRig {
	t.Helper()
	return newDragRig(t, true, func(c *ports.Config) {
		c.Animations.On = true
		if edit != nil {
			edit(c)
		}
	}, outs...)
}

func newDragRig(t *testing.T, animated bool, edit func(*ports.Config), outs ...ports.OutputInfo) *dragRig {
	t.Helper()
	cfg := config.Defaults()
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 10
	cfg.Terminal.AutoOpen = "off"
	if edit != nil {
		edit(&cfg)
	}
	epoch := &atomic.Value{}
	epoch.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return epoch.Load().(ports.SecurityState) }).Maybe()
	// Unbuffered: a send returns once core took the event, so it handled
	// every event sent before (settle relies on it).
	r := &dragRig{
		client: make(chan ports.ClientEvent), input: make(chan ports.InputEvent),
		output: make(chan ports.OutputEvent), commands: make(chan ports.ClientCommand, 4096),
		scenes: make(chan []ports.Scene, 1), epoch: epoch,
	}
	ch := core.Channels{Client: r.client, Input: r.input, Output: r.output, Commands: r.commands, Scenes: r.scenes}
	opts := core.Options{Security: gate}
	if animated {
		r.frames, r.clock = make(chan ports.OutputFrame), newStepClock(t)
		ch.Frames, opts.Clock = r.frames, r.clock.clock
		// An on variant that never saw a spring would be the off variant again.
		t.Cleanup(func() {
			if r.lands == 0 {
				t.Error("no spring ever ran: the animations-on variant checked nothing")
			}
		})
	}
	c, err := core.New(cfg, ch, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for _, o := range outs {
		r.output <- ports.OutputAdded{Info: o}
		r.outputs = append(r.outputs, o.Name)
		r.settle(t)
	}
	return r
}

// in sends a raw input event; the gate is on, so it is wrapped.
func (r *dragRig) in(ev ports.InputEvent) {
	send(r.input, ports.InputEvent(ports.SecurityInput{State: r.epoch.Load().(ports.SecurityState), Event: ev}))
}

// send fails the test binary fast instead of hanging when core is stuck.
func send[T any](ch chan T, v T) {
	select {
	case ch <- v:
	case <-time.After(5 * time.Second):
		panic("core stuck")
	}
}

// barrier is a window no test maps: a resize of it changes nothing but
// makes core publish.
const barrier ports.WindowID = 1 << 30

// settle returns the scene set once core handled every event sent so
// far. The second barrier is taken only after the first one published. An
// animated rig first moves its clock past every spring and flips each
// output, so the set is the settled one.
func (r *dragRig) settle(t *testing.T) []ports.Scene {
	t.Helper()
	if r.frames != nil {
		if _, ok := r.clock.settle(t, r.frames, r.scenes, r.outputs...); ok {
			r.lands++
		}
	}
	send(r.client, ports.ClientEvent(ports.WindowResized{ID: barrier}))
	send(r.client, ports.ClientEvent(ports.WindowResized{ID: barrier}))
	select {
	case s := <-r.scenes:
		r.last = s
	case <-time.After(5 * time.Second):
		t.Fatal("no scene")
	}
	return r.last
}

// drain returns the commands sent since the last drain.
func (r *dragRig) drain() []ports.ClientCommand {
	var out []ports.ClientCommand
	for {
		select {
		case v := <-r.commands:
			if s, ok := v.(ports.SecurityCommand); ok {
				v = s.Command
			}
			out = append(out, v)
		default:
			return out
		}
	}
}

func pointerSent(cmds []ports.ClientCommand) []ports.ClientCommand {
	var out []ports.ClientCommand
	for _, v := range cmds {
		switch v.(type) {
		case ports.PointerButtonTo, ports.PointerMotionTo, ports.ForwardKey:
			out = append(out, v)
		}
	}
	return out
}

func dragRect(t *testing.T, set []ports.Scene, output string, id ports.WindowID) ports.Rect {
	t.Helper()
	w, ok := windowIn(set, output, id)
	if !ok {
		t.Fatalf("window %d not on %s", id, output)
	}
	return w.Rect
}

func dragScene(set []ports.Scene, output string) ports.Scene {
	for _, s := range set {
		if s.Output == output {
			return s
		}
	}
	return ports.Scene{}
}

// columns lists the tiles left to right by x, then top to bottom.
func columnsOf(set []ports.Scene, output string) [][]ports.WindowID {
	s := dragScene(set, output)
	var wins []ports.SceneWindow
	for _, w := range s.Windows {
		if !w.Hidden && !w.Floating && !w.Popup {
			wins = append(wins, w)
		}
	}
	slices.SortFunc(wins, func(a, b ports.SceneWindow) int {
		if a.Rect.X != b.Rect.X {
			return a.Rect.X - b.Rect.X
		}
		return a.Rect.Y - b.Rect.Y
	})
	var out [][]ports.WindowID
	lastX := -1 << 30
	for _, w := range wins {
		if w.Rect.X != lastX {
			out = append(out, nil)
			lastX = w.Rect.X
		}
		out[len(out)-1] = append(out[len(out)-1], w.ID)
	}
	return out
}

var dragOut = ports.OutputInfo{Name: "A", Width: 1000, Height: 600}

func (r *dragRig) moveTo(x, y float64, msec uint32) {
	r.in(ports.PointerMotion{X: x, Y: y, Time: time.Duration(msec) * time.Millisecond})
}

func (r *dragRig) press(t *testing.T, button uint32, mods ports.Mods) {
	t.Helper()
	if mods != 0 {
		r.in(ports.KeyEvent{Keysym: "Super_L", Mods: mods, Pressed: true})
	}
	r.in(ports.PointerButton{Button: button, Pressed: true})
}

func TestCmdDragMovesFloatWithoutClientEvents(t *testing.T) {
	r := startDragRig(t, nil, dragOut)
	r.client <- ports.WindowMapped{ID: 1}
	r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 200, Height: 100}
	set := r.settle(t)
	f := dragRect(t, set, "A", 2)
	r.moveTo(float64(f.X+10), float64(f.Y+10), 1)
	r.settle(t)
	r.drain()
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(float64(f.X+60), float64(f.Y+40), 2)
	set = r.settle(t)
	got := dragRect(t, set, "A", 2)
	if got.X != f.X+50 || got.Y != f.Y+30 || got.W != f.W {
		t.Fatalf("moved to %+v from %+v", got, f)
	}
	if w, _ := windowIn(set, "A", 2); !w.Floating {
		t.Fatal("not floating")
	}
	r.in(ports.PointerButton{Button: 0x110})
	r.in(ports.KeyEvent{Keysym: "Super_L"})
	r.settle(t)
	cmds := r.drain()
	for _, v := range pointerSent(cmds) {
		if _, ok := v.(ports.PointerButtonTo); ok {
			t.Fatalf("client saw %#v", v)
		}
		if _, ok := v.(ports.PointerMotionTo); ok {
			t.Fatalf("client saw %#v", v)
		}
	}
	// It stays there: a free float now.
	r.moveTo(float64(f.X+61), float64(f.Y+41), 3)
	if got2 := dragRect(t, r.settle(t), "A", 2); got2 != got {
		t.Fatalf("moved after release: %+v", got2)
	}
	// A plain press goes to the client.
	r.drain()
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.settle(t)
	found := false
	for _, v := range r.drain() {
		if b, ok := v.(ports.PointerButtonTo); ok && b.ID == 2 && b.Pressed {
			found = true
		}
	}
	if !found {
		t.Fatal("plain press not forwarded")
	}
}

func TestDragEscapeRestoresFloat(t *testing.T) {
	r := startDragRig(t, nil, dragOut)
	r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 200, Height: 100}
	f := dragRect(t, r.settle(t), "A", 2)
	r.moveTo(float64(f.X+10), float64(f.Y+10), 1)
	r.settle(t)
	r.press(t, 0x111, ports.ModSuper)
	r.moveTo(float64(f.X+110), float64(f.Y+60), 2)
	if got := dragRect(t, r.settle(t), "A", 2); got.W != f.W+100 || got.H != f.H+50 || got.X != f.X {
		t.Fatalf("resize: %+v from %+v", got, f)
	}
	r.drain()
	r.in(ports.KeyEvent{Keysym: "Escape", Mods: ports.ModSuper, Pressed: true})
	if got := dragRect(t, r.settle(t), "A", 2); got != f {
		t.Fatalf("escape: %+v, want %+v", got, f)
	}
	r.in(ports.KeyEvent{Keysym: "Escape", Mods: ports.ModSuper})
	r.in(ports.PointerButton{Button: 0x111})
	r.settle(t)
	for _, v := range pointerSent(r.drain()) {
		t.Fatalf("client saw %#v", v)
	}
}

func TestDragTileZonesAndHints(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		dragTileZonesAndHints(t, startDragMode(t, animated, func(c *ports.Config) { c.Layout.MaxColumns = 3 }, dragOut))
	})
}

func dragTileZonesAndHints(t *testing.T, r *dragRig) {
	for id := ports.WindowID(1); id <= 3; id++ {
		r.client <- ports.WindowMapped{ID: id}
		r.settle(t)
	}
	set := r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{1}, {2}, {3}}, slices.Equal) {
		t.Fatal(got)
	}
	a, c := dragRect(t, set, "A", 1), dragRect(t, set, "A", 3)
	// Drag 1 onto the bottom band of 3: it stacks below 3.
	r.moveTo(float64(a.X+a.W/2), float64(a.Y+a.H/2), 1)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(float64(c.X+c.W/2), float64(c.Y+c.H-5), 2)
	set = r.settle(t)
	hints := dragScene(set, "A").DropHints
	if len(hints) != 1 || hints[0].Y+hints[0].H != c.Y+c.H || hints[0].W != c.W {
		t.Fatalf("hints %+v", hints)
	}
	// Tiles do not move until the drop.
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{1}, {2}, {3}}, slices.Equal) {
		t.Fatal(got)
	}
	// The drop itself must run a spring: the guard counts from here.
	r.lands = 0
	r.in(ports.PointerButton{Button: 0x110})
	set = r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{2}, {3, 1}}, slices.Equal) {
		t.Fatal(got)
	}
	if len(dragScene(set, "A").DropHints) != 0 {
		t.Fatal("hints left")
	}
	// Centre of 2 with stacked 1 dragged from its right: 1 becomes a
	// column in 2's place, 2 moves toward where 1 came from.
	b := dragRect(t, set, "A", 2)
	one := dragRect(t, set, "A", 1)
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 3)
	r.settle(t)
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.moveTo(float64(b.X+b.W/2), float64(b.Y+b.H/2), 4)
	if hints := dragScene(r.settle(t), "A").DropHints; len(hints) != 4 {
		t.Fatalf("swap outline %+v", hints)
	}
	// The drop itself must run a spring: the guard counts from here.
	r.lands = 0
	r.in(ports.PointerButton{Button: 0x110})
	set = r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{1}, {2}, {3}}, slices.Equal) {
		t.Fatal(got)
	}
	// Lone 3 on the centre of 2: the columns swap.
	b, c = dragRect(t, set, "A", 2), dragRect(t, set, "A", 3)
	r.moveTo(float64(c.X+c.W/2), float64(c.Y+c.H/2), 5)
	r.settle(t)
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.moveTo(float64(b.X+b.W/2), float64(b.Y+b.H/2), 6)
	r.settle(t)
	// The drop itself must run a spring: the guard counts from here.
	r.lands = 0
	r.in(ports.PointerButton{Button: 0x110})
	set = r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{1}, {3}, {2}}, slices.Equal) {
		t.Fatal(got)
	}
	// Escape mid tile drag: nothing moves, no hints.
	one = dragRect(t, set, "A", 1)
	b = dragRect(t, set, "A", 2)
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 7)
	r.settle(t)
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.moveTo(float64(b.X+b.W/2), float64(b.Y+5), 8)
	if len(dragScene(r.settle(t), "A").DropHints) == 0 {
		t.Fatal("no hint")
	}
	r.in(ports.KeyEvent{Keysym: "Escape", Mods: ports.ModSuper, Pressed: true})
	set = r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{1}, {3}, {2}}, slices.Equal) || len(dragScene(set, "A").DropHints) != 0 {
		t.Fatal(got, dragScene(set, "A").DropHints)
	}
}

// startDragMode is the drag rig with animations off, or on and landed by
// settle.
func startDragMode(t *testing.T, animated bool, edit func(*ports.Config), outs ...ports.OutputInfo) *dragRig {
	t.Helper()
	if animated {
		return startAnimatedDragRig(t, edit, outs...)
	}
	return startDragRig(t, func(c *ports.Config) {
		if edit != nil {
			edit(c)
		}
		c.Animations.On = false
	}, outs...)
}

func TestDragTileGapInsert(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		dragTileGapInsert(t, startDragMode(t, animated, func(c *ports.Config) { c.Layout.MaxColumns = 3 }, dragOut))
	})
}

func dragTileGapInsert(t *testing.T, r *dragRig) {
	for id := ports.WindowID(1); id <= 3; id++ {
		r.client <- ports.WindowMapped{ID: id}
		r.settle(t)
	}
	r.client <- ports.WindowMapped{ID: 4}
	r.settle(t)
	// Stack 4 under 3 with the keyboard, then put 4 between 1 and 2.
	r.in(ports.KeyEvent{Keysym: "bracketleft", Mods: ports.ModSuper, Pressed: true})
	r.in(ports.KeyEvent{Keysym: "bracketleft", Mods: ports.ModSuper})
	r.in(ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper, Pressed: true})
	r.in(ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper})
	r.in(ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper, Pressed: true})
	r.in(ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper})
	set := r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{1}, {2}, {3, 4}}, slices.Equal) {
		t.Fatal(got)
	}
	one, two, four := dragRect(t, set, "A", 1), dragRect(t, set, "A", 2), dragRect(t, set, "A", 4)
	gapX := (one.X + one.W + two.X) / 2
	r.moveTo(float64(four.X+four.W/2), float64(four.Y+four.H/2), 1)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(float64(gapX), float64(one.Y+one.H/2), 2)
	hints := dragScene(r.settle(t), "A").DropHints
	if len(hints) != 1 || hints[0].H != one.H || hints[0].X+hints[0].W/2 != gapX {
		t.Fatalf("gap hint %+v", hints)
	}
	r.in(ports.PointerButton{Button: 0x110})
	if got := columnsOf(r.settle(t), "A"); !slices.EqualFunc(got[:2], [][]ports.WindowID{{1}, {4}}, slices.Equal) {
		t.Fatal(got)
	}
}

// release sends the button release and returns the scene set core
// publishes for it, without moving the clock (settle would).
func (r *dragRig) release(t *testing.T) []ports.Scene {
	t.Helper()
	r.in(ports.PointerButton{Button: 0x110})
	send(r.client, ports.ClientEvent(ports.WindowResized{ID: barrier}))
	send(r.client, ports.ClientEvent(ports.WindowResized{ID: barrier}))
	select {
	case r.last = <-r.scenes:
	case <-time.After(5 * time.Second):
		t.Fatal("no scene")
	}
	return r.last
}

// A dropped tile slides from where the drag scene drew it (its own slot:
// a tile does not follow the pointer) and its neighbours re-flow; the
// client gets one configure, for the final size. With animations off the
// drop is instant.
func TestDragDropTileSlides(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r := startDragMode(t, animated, func(c *ports.Config) { c.Layout.MaxColumns = 3 }, dragOut)
		for id := ports.WindowID(1); id <= 3; id++ {
			r.client <- ports.WindowMapped{ID: id}
			r.settle(t)
		}
		set := r.settle(t)
		one0, two0, three0 := dragRect(t, set, "A", 1), dragRect(t, set, "A", 2), dragRect(t, set, "A", 3)
		r.moveTo(float64(one0.X+one0.W/2), float64(one0.Y+one0.H/2), 1)
		r.settle(t)
		r.press(t, 0x110, ports.ModSuper)
		// The bottom band of 3: 1 stacks below it.
		r.moveTo(float64(three0.X+three0.W/2), float64(three0.Y+three0.H-5), 2)
		set = r.settle(t)
		if got := dragRect(t, set, "A", 1); got != one0 {
			t.Fatalf("tile left its slot during the drag: %+v, was %+v", got, one0)
		}
		r.drain()
		set = r.release(t)
		if len(dragScene(set, "A").DropHints) != 0 {
			t.Fatal("hints left")
		}
		if !animated {
			if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{2}, {3, 1}}, slices.Equal) {
				t.Fatal(got)
			}
			// Instant: the scene right after the drop is the settled one.
			if got := dragRect(t, set, "A", 1); got == one0 || got.X != dragRect(t, r.settle(t), "A", 1).X {
				t.Fatalf("not dropped at once: %+v", got)
			}
			return
		}
		// Right after the release the windows are still where the drag
		// scene drew them.
		for id, want := range map[ports.WindowID]ports.Rect{1: one0, 2: two0, 3: three0} {
			if got := dragRect(t, set, "A", id); got != want {
				t.Fatalf("window %d at %+v right after the drop, want its old %+v", id, got, want)
			}
		}
		mid, ok := r.clock.flip(t, r.frames, r.scenes, 30*time.Millisecond, "A")
		if !ok {
			t.Fatal("no frame after the drop: nothing animates")
		}
		r.lands++
		// The dropped tile is between its old slot and its new one, and the
		// column it joined re-flows too.
		got := dragRect(t, mid, "A", 1)
		if got == one0 || got.X <= one0.X && got.Y <= one0.Y {
			t.Fatalf("window 1 at %+v on the first frame, still at its old %+v", got, one0)
		}
		if got2 := dragRect(t, mid, "A", 2); got2 == two0 {
			t.Fatalf("neighbour 2 did not start moving: %+v", got2)
		}
		set = r.settle(t)
		if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{2}, {3, 1}}, slices.Equal) {
			t.Fatal(got)
		}
		want := map[ports.WindowID]ports.Rect{}
		for id := ports.WindowID(1); id <= 3; id++ {
			want[id] = dragRect(t, set, "A", id)
		}
		if want[1].Y <= want[3].Y || want[1].X != want[3].X {
			t.Fatalf("1 is not stacked below 3 once settled: %+v", want)
		}
		// A window is configured at most once by the drop, to its settled
		// size, never to a size along the slide; the dropped tile always.
		sizes := map[ports.WindowID][][2]int{}
		for _, v := range r.drain() {
			if v, ok := v.(ports.ConfigureWindow); ok {
				sizes[v.ID] = append(sizes[v.ID], [2]int{v.Width, v.Height})
			}
		}
		for id, rect := range want {
			got := sizes[id]
			if len(got) > 1 || len(got) == 1 && got[0] != [2]int{rect.W, rect.H} || id == 1 && len(got) != 1 {
				t.Fatalf("configures of window %d %v, want at most one at %dx%d (exactly one for window 1)", id, got, rect.W, rect.H)
			}
		}
	})
}

func TestDragFixedNoGapZone(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		dragFixedNoGapZone(t, startDragMode(t, animated, func(c *ports.Config) { c.Layout.MaxColumns = 3; c.Layout.Overflow = "fixed" }, dragOut))
	})
}

func dragFixedNoGapZone(t *testing.T, r *dragRig) {
	for id := ports.WindowID(1); id <= 2; id++ {
		r.client <- ports.WindowMapped{ID: id}
		r.settle(t)
	}
	set := r.settle(t)
	one, two := dragRect(t, set, "A", 1), dragRect(t, set, "A", 2)
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(float64((one.X+one.W+two.X)/2), float64(one.Y+one.H/2), 2)
	if hints := dragScene(r.settle(t), "A").DropHints; len(hints) != 0 {
		t.Fatalf("fixed gap hint %+v", hints)
	}
	// Top band of 2 stacks 1 above it.
	r.moveTo(float64(two.X+two.W/2), float64(two.Y+3), 3)
	r.settle(t)
	// The drop itself must run a spring: the guard counts from here.
	r.lands = 0
	r.in(ports.PointerButton{Button: 0x110})
	if got := columnsOf(r.settle(t), "A"); !slices.EqualFunc(got, [][]ports.WindowID{{1, 2}}, slices.Equal) {
		t.Fatal(got)
	}
}

func TestDragAcrossOutputs(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		dragAcrossOutputs(t, startDragMode(t, animated, nil, dragOut, ports.OutputInfo{Name: "B", Width: 800, Height: 600}))
	})
}

func dragAcrossOutputs(t *testing.T, r *dragRig) {
	r.client <- ports.WindowMapped{ID: 1}
	r.client <- ports.WindowMapped{ID: 2}
	r.settle(t)
	// Put 3 on B.
	r.moveTo(1500, 300, 1)
	r.settle(t)
	r.client <- ports.WindowMapped{ID: 3}
	set := r.settle(t)
	if _, ok := windowIn(set, "B", 3); !ok {
		t.Fatal("3 not on B")
	}
	one := dragRect(t, set, "A", 1)
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 2)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(1400, 300, 3)
	set = r.settle(t)
	if len(dragScene(set, "B").DropHints) == 0 || len(dragScene(set, "A").DropHints) != 0 {
		t.Fatalf("hints A %+v B %+v", dragScene(set, "A").DropHints, dragScene(set, "B").DropHints)
	}
	r.lands = 0
	r.in(ports.PointerButton{Button: 0x110})
	set = r.settle(t)
	if _, ok := windowIn(set, "B", 1); !ok {
		t.Fatal("1 not on B")
	}
	if w, _ := windowIn(set, "B", 1); !w.Focused {
		t.Fatal("1 not focused")
	}
}

func TestClientMoveRequest(t *testing.T) {
	r := startDragRig(t, nil, dragOut)
	r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 200, Height: 100}
	f := dragRect(t, r.settle(t), "A", 2)
	// Without a held button: ignored.
	r.client <- ports.WindowMoveRequest{ID: 2}
	r.moveTo(float64(f.X+10), float64(f.Y+10), 1)
	r.settle(t)
	r.moveTo(float64(f.X+20), float64(f.Y+10), 2)
	if got := dragRect(t, r.settle(t), "A", 2); got != f {
		t.Fatalf("moved without a button: %+v", got)
	}
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.settle(t)
	r.client <- ports.WindowMoveRequest{ID: 2}
	r.settle(t)
	r.drain()
	r.moveTo(float64(f.X+60), float64(f.Y+10), 3)
	if got := dragRect(t, r.settle(t), "A", 2); got.X != f.X+40 {
		t.Fatalf("move request: %+v from %+v", got, f)
	}
	r.in(ports.PointerButton{Button: 0x110})
	r.settle(t)
	for _, v := range pointerSent(r.drain()) {
		t.Fatalf("client saw %#v", v)
	}
}

func TestLockClearsDrag(t *testing.T) {
	r := startDragRig(t, nil, dragOut)
	r.client <- ports.WindowMapped{ID: 1}
	r.client <- ports.WindowMapped{ID: 2}
	set := r.settle(t)
	one, two := dragRect(t, set, "A", 1), dragRect(t, set, "A", 2)
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(float64(two.X+two.W/2), float64(two.Y+5), 2)
	if len(dragScene(r.settle(t), "A").DropHints) == 0 {
		t.Fatal("no hint")
	}
	locked := ports.SecurityState{Generation: 1, Protected: true}
	r.epoch.Store(locked)
	r.client <- ports.SessionLockChanged{State: locked}
	set = r.settle(t)
	if len(dragScene(set, "A").DropHints) != 0 {
		t.Fatal("hints while locked")
	}
	unlocked := ports.SecurityState{Generation: 2}
	r.epoch.Store(unlocked)
	r.client <- ports.SessionLockChanged{State: unlocked}
	r.settle(t)
	r.in(ports.PointerButton{Button: 0x110})
	set = r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{1}, {2}}, slices.Equal) {
		t.Fatal(got)
	}
}

func TestDragEdgeScroll(t *testing.T) {
	both(t, func(t *testing.T, animated bool) { dragEdgeScroll(t, startDragMode(t, animated, nil, dragOut)) })
}

func dragEdgeScroll(t *testing.T, r *dragRig) {
	for id := ports.WindowID(1); id <= 4; id++ {
		r.client <- ports.WindowMapped{ID: id}
		r.settle(t)
	}
	// Back to the first column: 3 and 4 are off screen to the right.
	for range 3 {
		r.in(ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper})
	}
	set := r.settle(t)
	one := dragRect(t, set, "A", 1)
	if one.X != 10 {
		t.Fatalf("%+v", one)
	}
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1000)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(995, 300, 1001)
	first := dragRect(t, r.settle(t), "A", 1)
	if first.X >= one.X {
		t.Fatalf("no scroll: %+v", first)
	}
	// Under 250 ms: no second step.
	r.moveTo(996, 300, 1100)
	if got := dragRect(t, r.settle(t), "A", 1); got != first {
		t.Fatalf("scrolled too soon: %+v", got)
	}
	r.moveTo(997, 300, 1300)
	second := dragRect(t, r.settle(t), "A", 1)
	r.moveTo(998, 300, 1600)
	r.moveTo(999, 300, 1900)
	end := dragRect(t, r.settle(t), "A", 1)
	r.moveTo(998, 300, 2200)
	if got := dragRect(t, r.settle(t), "A", 1); second.X >= first.X || got != end {
		t.Fatalf("first %+v second %+v end %+v got %+v", first, second, end, got)
	}
	r.in(ports.KeyEvent{Keysym: "Escape", Mods: ports.ModSuper, Pressed: true})
	r.settle(t)
}

func TestDragFullFixedStacksInOutlinedColumn(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		dragFullFixedStacksInOutlinedColumn(t, startDragMode(t, animated, func(c *ports.Config) { c.Layout.MaxColumns = 3; c.Layout.Overflow = "fixed" }, dragOut))
	})
}

func dragFullFixedStacksInOutlinedColumn(t *testing.T, r *dragRig) {
	for id := ports.WindowID(1); id <= 3; id++ {
		r.client <- ports.WindowMapped{ID: id}
		r.settle(t)
	}
	set := r.settle(t)
	one, two := dragRect(t, set, "A", 1), dragRect(t, set, "A", 2)
	// 4 joins 1's column from the top band of 1.
	r.client <- ports.WindowMapped{ID: 4}
	set = r.settle(t)
	four := dragRect(t, set, "A", 4)
	r.moveTo(float64(four.X+four.W/2), float64(four.Y+four.H/2), 1)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+3), 2)
	r.settle(t)
	// The drop itself must run a spring: the guard counts from here.
	r.lands = 0
	r.in(ports.PointerButton{Button: 0x110})
	set = r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]ports.WindowID{{4, 1}, {2}, {3}}, slices.Equal) {
		t.Fatal(got)
	}
	one, two = dragRect(t, set, "A", 1), dragRect(t, set, "A", 2)
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 3)
	r.settle(t)
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.moveTo(float64(two.X+two.W/2), float64(two.Y+two.H/2), 4)
	r.settle(t)
	// The drop itself must run a spring: the guard counts from here.
	r.lands = 0
	r.in(ports.PointerButton{Button: 0x110})
	if got := columnsOf(r.settle(t), "A"); !slices.EqualFunc(got, [][]ports.WindowID{{4}, {2, 1}, {3}}, slices.Equal) {
		t.Fatal(got)
	}
}

func TestPressAfterCancelGrabs(t *testing.T) {
	r := startDragRig(t, nil, dragOut)
	r.client <- ports.WindowMapped{ID: 1}
	r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 200, Height: 100}
	f := dragRect(t, r.settle(t), "A", 2)
	r.moveTo(float64(f.X+10), float64(f.Y+10), 1)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.in(ports.KeyEvent{Keysym: "Escape", Mods: ports.ModSuper, Pressed: true})
	r.in(ports.KeyEvent{Keysym: "Escape", Mods: ports.ModSuper})
	r.in(ports.KeyEvent{Keysym: "Super_L"})
	r.settle(t)
	r.drain()
	// Left is still held, swallowed. A right click on 2 goes to 2, and so
	// does its release after the pointer left it.
	r.in(ports.PointerButton{Button: 0x111, Pressed: true})
	r.moveTo(5, 5, 2)
	r.in(ports.PointerButton{Button: 0x111})
	r.settle(t)
	var got []ports.PointerButtonTo
	for _, v := range r.drain() {
		if b, ok := v.(ports.PointerButtonTo); ok {
			got = append(got, b)
		}
	}
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 2 || got[1].Pressed {
		t.Fatalf("%+v", got)
	}
}

func TestClientResizeWithoutEdgesIgnored(t *testing.T) {
	r := startDragRig(t, nil, dragOut)
	r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 200, Height: 100}
	f := dragRect(t, r.settle(t), "A", 2)
	r.moveTo(float64(f.X+10), float64(f.Y+10), 1)
	r.settle(t)
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.client <- ports.WindowMoveRequest{ID: 2, Resize: true}
	r.moveTo(float64(f.X+60), float64(f.Y+60), 2)
	if got := dragRect(t, r.settle(t), "A", 2); got != f {
		t.Fatalf("%+v from %+v", got, f)
	}
}

func TestClientDragUsesLastPress(t *testing.T) {
	r := startDragRig(t, nil, dragOut)
	r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 200, Height: 100}
	f := dragRect(t, r.settle(t), "A", 2)
	r.moveTo(float64(f.X+10), float64(f.Y+10), 1)
	r.settle(t)
	r.in(ports.PointerButton{Button: 0x111, Pressed: true})
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.client <- ports.WindowMoveRequest{ID: 2}
	r.moveTo(float64(f.X+60), float64(f.Y+10), 2)
	r.settle(t)
	// Releasing left ends the drag: the window stays put afterwards.
	r.in(ports.PointerButton{Button: 0x110})
	r.moveTo(float64(f.X+90), float64(f.Y+10), 3)
	if got := dragRect(t, r.settle(t), "A", 2); got.X != f.X+50 {
		t.Fatalf("%+v from %+v", got, f)
	}
}

func TestEdgeScrollPublishesWithoutTarget(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		edgeScrollPublishesWithoutTarget(t, startDragMode(t, animated, nil, dragOut))
	})
}

func edgeScrollPublishesWithoutTarget(t *testing.T, r *dragRig) {
	for id := ports.WindowID(1); id <= 4; id++ {
		r.client <- ports.WindowMapped{ID: id}
		r.settle(t)
	}
	for range 3 {
		r.in(ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "Left", Mods: ports.ModSuper})
	}
	// Drag 2 into the top gap, then along it to the right edge: no drop
	// target before or after, but the view scrolls.
	two := dragRect(t, r.settle(t), "A", 2)
	edge := float64(dragOut.Width - 30)
	if float64(two.X+two.W) <= edge {
		t.Fatalf("2 does not reach the edge: %+v", two)
	}
	r.moveTo(float64(two.X+two.W/2), float64(two.Y+two.H/2), 1000)
	r.settle(t)
	r.press(t, 0x110, ports.ModSuper)
	r.moveTo(float64(two.X+two.W/2), 5, 1001)
	if hints := dragScene(r.settle(t), "A").DropHints; len(hints) != 0 {
		t.Fatalf("hint in the gap %+v", hints)
	}
	r.moveTo(edge, 5, 1300)
	// An axis event publishes nothing mid-drag: once core took it, the
	// motion's scene, if any, is in the channel.
	r.in(ports.PointerAxis{})
	select {
	case set := <-r.scenes:
		if got := dragRect(t, set, "A", 2); got.X >= two.X {
			t.Fatalf("not scrolled: %+v", got)
		}
	default:
		t.Fatal("edge scroll not published")
	}
}

func TestModsFollowKeys(t *testing.T) {
	r := startDragRig(t, nil, dragOut)
	r.client <- ports.WindowMapped{ID: 1}
	r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 200, Height: 100}
	f := dragRect(t, r.settle(t), "A", 2)
	r.moveTo(float64(f.X+10), float64(f.Y+10), 1)
	r.settle(t)
	// Super pressed then released: a click is a plain click again.
	r.in(ports.KeyEvent{Keysym: "Super_L", Mods: ports.ModSuper, Pressed: true})
	r.in(ports.KeyEvent{Keysym: "Super_L"})
	r.settle(t)
	r.drain()
	r.in(ports.PointerButton{Button: 0x110, Pressed: true})
	r.settle(t)
	found := false
	for _, v := range r.drain() {
		if b, ok := v.(ports.PointerButtonTo); ok && b.ID == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("click after cmd release not forwarded")
	}
}
