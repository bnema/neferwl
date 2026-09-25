package core

import (
	"crypto/rand"
	"fmt"
	"slices"

	"github.com/bnema/nefertty/internal/ports"
)

// Slots of declared workspaces (ADR 011): workspace.<name>.column.N reserves
// column N for the window of one command. The slot, not the window, has the
// identity: nefertty spawns the command with SlotEnv set to a random token,
// and the first window of a client carrying that token fills the slot.
//
// Only the token of the pending spawn is accepted: once the slot is filled,
// or the command changes, other windows with that token (children of the slot
// app inherit its environment) open as normal windows.
//
// A slot is spawned at startup, and again when the user shows its workspace
// while it is empty. Nothing is relaunched on its own.

type slotKey struct {
	workspace string
	index     int
}

type slotState struct {
	argv   []string
	width  Width
	token  string   // SlotEnv value of the pending spawn; "" when none
	window WindowID // 0 while empty
	// stale is set when the workspace is shown while the spawn is pending.
	// Shown again still pending, the command is deemed failed and respawned;
	// a slow app therefore gets one show to map its window.
	stale bool
}

func (s *slotState) pending() bool { return s.token != "" }

// slotSpec is one parsed slot of the config.
type slotSpec struct {
	key   slotKey
	argv  []string
	width Width
}

func parseSlots(ws []ports.WorkspaceConfig) ([]slotSpec, error) {
	var specs []slotSpec
	for _, w := range ws {
		for _, s := range w.Slots {
			width, err := ParseWidth(s.Width)
			if err != nil {
				return nil, fmt.Errorf("workspace %s column %d: %w", w.Name, s.Index, err)
			}
			if len(s.Argv) == 0 || s.Index < 1 {
				return nil, fmt.Errorf("workspace %s column %d: invalid slot", w.Name, s.Index)
			}
			specs = append(specs, slotSpec{key: slotKey{w.Name, s.Index}, argv: slices.Clone(s.Argv), width: width})
		}
	}
	return specs, nil
}

// updateSlots applies the config and returns the slots to spawn: new ones,
// and empty ones whose command changed. A slot dropped from config keeps its
// window as a normal column. Call before named workspaces are applied, so a
// renamed or dropped workspace can still be found.
func (c *Core) updateSlots(specs []slotSpec) []slotKey {
	want := map[slotKey]slotSpec{}
	for _, s := range specs {
		want[s.key] = s
	}
	for key := range c.slots {
		if _, ok := want[key]; ok {
			continue
		}
		if _, w := c.byName(key.workspace); w != nil {
			w.unslot(key.index)
		}
		delete(c.slots, key)
	}
	var spawn []slotKey
	for _, s := range specs {
		st, ok := c.slots[s.key]
		if !ok {
			c.slots[s.key] = &slotState{argv: s.argv, width: s.width}
			spawn = append(spawn, s.key)
			continue
		}
		if st.width != s.width {
			st.width = s.width
			if _, w := c.byName(s.key.workspace); w != nil {
				w.setSlotWidth(s.key.index, s.width)
			}
		}
		if !slices.Equal(st.argv, s.argv) {
			st.argv = s.argv
			if st.window == 0 {
				// A pending spawn ran the old command: its window opens as
				// a normal one; the new command replaces it.
				st.token = ""
				spawn = append(spawn, s.key)
			}
		}
	}
	return spawn
}

// spawnSlot starts the slot's command with a fresh random token.
func (c *Core) spawnSlot(key slotKey) ports.SpawnRequest {
	st := c.slots[key]
	st.token, st.stale = rand.Text(), false
	return ports.SpawnRequest{Argv: slices.Clone(st.argv), Env: []string{ports.SlotEnv + "=" + st.token}}
}

// placeSlotWindow puts a mapped window carrying the pending token of a slot
// into that slot, without focus. It returns false for any other window, which
// then follows the normal rules.
func (c *Core) placeSlotWindow(id WindowID, token string) bool {
	for key, st := range c.slots {
		if !st.pending() || st.token != token || st.window != 0 {
			continue
		}
		sc, w := c.byName(key.workspace)
		if w == nil {
			return false
		}
		st.window, st.token = id, ""
		w.AddSlotWindow(id, key.index, st.width)
		sc.mon.normalize()
		return true
	}
	return false
}

// releaseSlots empties slots whose window is gone or no longer in its slot
// column (closed, or moved to another workspace by the user).
func (c *Core) releaseSlots() {
	for key, st := range c.slots {
		if st.window == 0 {
			continue
		}
		sc, w := c.byName(key.workspace)
		// A slot window away in its own fullscreen workspace keeps its slot.
		if w == nil || !w.inSlot(st.window, key.index) && !sc.mon.awayInSlot(w, st.window, key.index) {
			st.window = 0
		}
	}
}

// refill returns the empty slots of the workspace on screen to spawn. A slot
// still pending for the second show in a row is respawned: its command
// failed, exited or handed off to another instance without a window.
func (c *Core) refill() []slotKey {
	name := c.cur().mon.Current().Name
	if name == "" {
		return nil
	}
	var keys []slotKey
	for key, st := range c.slots {
		switch {
		case key.workspace != name || st.window != 0:
		case st.pending() && !st.stale:
			st.stale = true
		default:
			st.token = ""
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b slotKey) int { return a.index - b.index })
	return keys
}

// anyPending reports whether a slot waits for its window.
func (c *Core) anyPending() bool {
	if len(c.terms) > 0 {
		return true
	}
	for _, st := range c.slots {
		if st.pending() {
			return true
		}
	}
	return false
}
