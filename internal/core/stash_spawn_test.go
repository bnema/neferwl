package core_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// stashSpawnRig has a stash bind on Alt+f and, as in the defaults, foot as
// the terminal. Tiles map without any automatic terminal.
func stashSpawnRig(t *testing.T, edit func(*ports.Config)) *multiRig {
	t.Helper()
	return startMulti(t, func(c *ports.Config) {
		c.Binds["Alt+Ctrl+Shift+f"] = "stash firefox --new-window"
		c.Stash.Capture = false
		if edit != nil {
			edit(c)
		}
	}, left)
}

func slotToken(t *testing.T, req ports.SpawnRequest) string {
	t.Helper()
	if len(req.Env) != 1 || len(req.Env[0]) <= len(ports.SlotEnv)+1 || req.Env[0][:len(ports.SlotEnv)+1] != ports.SlotEnv+"=" {
		t.Fatalf("no slot token: %v", req.Env)
	}
	return req.Env[0][len(ports.SlotEnv)+1:]
}

// pendingSent drains the command queue and reports the last SlotsPending.
func pendingSent(r *multiRig) (pending, sent bool) {
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.SlotsPending); ok {
			pending, sent = v.Pending, true
		}
	}
	return pending, sent
}

func sceneWindow(set []ports.Scene, id ports.WindowID) (ports.SceneWindow, bool) {
	for _, s := range set {
		for _, w := range s.Windows {
			if w.ID == id {
				return w, true
			}
		}
	}
	return ports.SceneWindow{}, false
}

// The window of a stash bind opens in the stash of the workspace the key
// was pressed on, shown and focused, even when the user moved on before it
// mapped; toggle-window-stash then tiles it as a new column.
func TestStashBindOpensWindowInStash(t *testing.T) {
	r := stashSpawnRig(t, nil)
	r.mapWindow(t, 1)
	r.key(t, "f", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	req := receive(t, r.spawn)
	if !slices.Equal(req.Argv, []string{"firefox", "--new-window"}) {
		t.Fatalf("argv %v", req.Argv)
	}
	token := slotToken(t, req)
	if pending, sent := pendingSent(r); !sent || !pending {
		t.Fatal("SlotsPending{true} was not sent")
	}
	r.key(t, "Next", ports.ModAlt) // focus moves to workspace 2
	r.client <- ports.WindowMapped{ID: 2, Slot: token}
	set := receive(t, r.scenes)
	if w, ok := sceneWindow(set, 2); ok && !w.Hidden {
		t.Fatalf("window shown on the workspace the user moved to: %+v", w)
	}
	set = r.key(t, "Prior", ports.ModAlt)
	w, ok := sceneWindow(set, 2)
	if !ok || w.Hidden || !w.Floating || !w.Focused {
		t.Fatalf("stashed window %+v", w)
	}
	if w1, _ := sceneWindow(set, 1); w1.Focused {
		t.Fatal("tile kept the focus")
	}
	if pending, sent := pendingSent(r); !sent || pending {
		t.Fatalf("pending flag not cleared: %v %v", pending, sent)
	}
	// Unstashing tiles it as a column of its own.
	set = r.key(t, "s", ports.ModAlt|ports.ModShift)
	if w, ok := sceneWindow(set, 2); !ok || w.Floating || w.Hidden || !w.Focused {
		t.Fatalf("unstashed window %+v", w)
	}
	if len(r.spawn) != 0 {
		t.Fatal("unexpected spawn")
	}
}

func TestStashBindRunsEveryCommand(t *testing.T) {
	r := stashSpawnRig(t, func(c *ports.Config) { c.Binds["Alt+Ctrl+Shift+f"] = "stash one; two" })
	r.key(t, "f", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	a, b := receive(t, r.spawn), receive(t, r.spawn)
	if a.Argv[0] != "one" || b.Argv[0] != "two" || slotToken(t, a) == slotToken(t, b) {
		t.Fatal(a, b)
	}
}

// With an empty stash, toggle-stash-visible opens the terminal in it, once
// while it is awaited.
func TestToggleEmptyStashOpensTerminal(t *testing.T) {
	r := stashSpawnRig(t, nil)
	r.mapWindow(t, 1)
	r.key(t, "s", ports.ModAlt)
	req := receive(t, r.spawn)
	if len(req.Argv) != 1 || req.Argv[0] != "foot" {
		t.Fatalf("argv %v", req.Argv)
	}
	token := slotToken(t, req)
	if pending, sent := pendingSent(r); !sent || !pending {
		t.Fatal("SlotsPending{true} was not sent")
	}
	r.key(t, "s", ports.ModAlt)
	if len(r.spawn) != 0 {
		t.Fatal("second terminal while the first is awaited")
	}
	r.client <- ports.WindowMapped{ID: 2, Slot: token}
	set := receive(t, r.scenes)
	if w, ok := sceneWindow(set, 2); !ok || w.Hidden || !w.Floating || !w.Focused {
		t.Fatalf("terminal %+v", w)
	}
	// The stash has a window now: the toggle hides it, no spawn.
	set = r.key(t, "s", ports.ModAlt)
	if w, _ := sceneWindow(set, 2); !w.Hidden {
		t.Fatalf("stash not hidden: %+v", w)
	}
	if len(r.spawn) != 0 {
		t.Fatal("spawned for a stash holding a window")
	}
}

func TestToggleNonEmptyStashDoesNotSpawn(t *testing.T) {
	r := stashSpawnRig(t, nil)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.key(t, "s", ports.ModAlt|ports.ModShift) // 2 stashed
	r.key(t, "s", ports.ModAlt)
	r.key(t, "s", ports.ModAlt)
	if len(r.spawn) != 0 {
		t.Fatal("spawned for a non-empty stash")
	}
}

func TestToggleEmptyStashRespectsOption(t *testing.T) {
	r := stashSpawnRig(t, func(c *ports.Config) { c.Stash.SpawnTerminal = false })
	r.mapWindow(t, 1)
	r.key(t, "s", ports.ModAlt)
	if len(r.spawn) != 0 {
		t.Fatal("spawned with stash.spawn-terminal off")
	}
	r = stashSpawnRig(t, func(c *ports.Config) { c.Terminal.Command = nil })
	r.mapWindow(t, 1)
	r.key(t, "s", ports.ModAlt)
	if len(r.spawn) != 0 {
		t.Fatal("spawned without a terminal command")
	}
}

func TestToggleEmptyStashInOverviewDoesNotSpawn(t *testing.T) {
	r := stashSpawnRig(t, nil)
	r.mapWindow(t, 1)
	r.key(t, "o", ports.ModAlt)
	r.key(t, "s", ports.ModAlt)
	if len(r.spawn) != 0 {
		t.Fatal("spawned from the overview")
	}
}

// A four-finger swipe down on an empty stash is toggle-stash-visible: with
// stash.spawn-terminal it opens the terminal in the stash, once while that
// one is awaited, and the window that maps lands in the stash.
func TestFourFingerSwipeEmptyStashOpensTerminal(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Stash.SpawnTerminal = true })
	r.four(t, 0, 40, 10)
	req := receive(t, r.spawn)
	if len(req.Argv) != 1 || req.Argv[0] != "foot" {
		t.Fatalf("argv %v", req.Argv)
	}
	token := slotToken(t, req)
	if pending, sent := pendingSent(r.multiRig); !sent || !pending {
		t.Fatal("SlotsPending{true} was not sent")
	}
	r.four(t, 0, 40, 10)
	if len(r.spawn) != 0 {
		t.Fatal("second terminal while the first is awaited")
	}
	r.client <- ports.WindowMapped{ID: 2, Slot: token}
	if w, ok := sceneWindow(receive(t, r.scenes), 2); !ok || w.Hidden || !w.Floating || !w.Focused {
		t.Fatalf("terminal %+v", w)
	}
}

func TestFourFingerSwipeEmptyStashRespectsOption(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Stash.SpawnTerminal = false })
	r.four(t, 0, 40, 10)
	if len(r.spawn) != 0 {
		t.Fatal("spawned with stash.spawn-terminal off")
	}
}

// The terminal of a four-finger swipe shows no workspace: the empty slots of
// the workspace on screen are not refilled, as with the bind.
func TestFourFingerSwipeEmptyStashDoesNotRefillSlots(t *testing.T) {
	r := startSlots(t)
	r.fill(t)
	r.press(t, "d") // dev on screen, its slots filled
	r.client <- ports.WindowUnmapped{ID: 2}
	sceneMatch(t, r.scenes, func(s ports.Scene) bool { return len(s.Windows) == 1 })
	noSpawn(t, r.spawn) // nothing relaunches on its own
	var at time.Duration
	r.input <- ports.SwipeBegin{Fingers: 4, Time: at}
	for range 10 {
		at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DY: 40, Time: at}
	}
	r.input <- ports.SwipeEnd{Time: at}
	req := receive(t, r.spawn)
	if len(req.Argv) != 1 || req.Argv[0] != "foot" {
		t.Fatalf("argv %v, want the stash terminal", req.Argv)
	}
	r.sync(t)
	noSpawn(t, r.spawn) // the empty slot of dev stays empty
}
