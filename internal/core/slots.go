package core

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/bnema/nefertty/internal/ports"
)

// Slots of declared workspaces (ADR 011): workspace.<name>.column.N reserves
// column N for the window of one command. The slot, not the window, has the
// identity: nefertty spawns the command with SlotEnv set to a token, and the
// first window of a client carrying that token fills the slot.
//
// A slot is spawned at startup and when its workspace is shown while it is
// empty and no spawn is pending. Nothing is relaunched on its own.

type slotKey struct {
	workspace string
	index     int
}

type slotState struct {
	argv    []string
	width   Width
	token   string   // SlotEnv value of the last spawn; "" before the first
	window  WindowID // 0 while empty
	pending bool     // spawned, window not mapped yet
}

type slots struct {
	byKey map[slotKey]*slotState
	// byToken maps spawn tokens (current and past) to their slot, so a
	// late window of an old spawn still lands in its slot.
	byToken map[string]slotKey
	next    uint64
}

func newSlots() slots {
	return slots{byKey: map[slotKey]*slotState{}, byToken: map[string]slotKey{}}
}

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

// updateSlots applies the config. It returns the new slots, which the caller
// spawns; changed commands are respawned only when their slot is empty.
// A slot dropped from config keeps its window as a normal column.
func (c *Core) updateSlots(specs []slotSpec) []slotKey {
	want := map[slotKey]slotSpec{}
	for _, s := range specs {
		want[s.key] = s
	}
	for key := range c.slots.byKey {
		if _, ok := want[key]; ok {
			continue
		}
		if w := c.ws.byName(key.workspace); w != nil {
			w.unslot(key.index)
		}
		c.forgetTokens(key)
		delete(c.slots.byKey, key)
	}
	var spawn []slotKey
	for _, s := range specs {
		st, ok := c.slots.byKey[s.key]
		if !ok {
			c.slots.byKey[s.key] = &slotState{argv: s.argv, width: s.width}
			spawn = append(spawn, s.key)
			continue
		}
		if st.width != s.width {
			st.width = s.width
			if w := c.ws.byName(s.key.workspace); w != nil {
				w.setSlotWidth(s.key.index, s.width)
			}
		}
		if !slices.Equal(st.argv, s.argv) {
			st.argv = s.argv
			if st.window == 0 {
				// A pending spawn ran the old command: its window is no
				// longer the slot's; the new command replaces it.
				c.forgetTokens(s.key)
				st.pending = false
				spawn = append(spawn, s.key)
			}
		}
	}
	return spawn
}

// forgetTokens drops the spawn tokens of a slot: their windows open as normal ones.
func (c *Core) forgetTokens(key slotKey) {
	for tok, k := range c.slots.byToken {
		if k == key {
			delete(c.slots.byToken, tok)
		}
	}
}

// spawnSlot starts the slot's command with a fresh token.
func (c *Core) spawnSlot(key slotKey) ports.SpawnRequest {
	st := c.slots.byKey[key]
	c.slots.next++
	st.token = strconv.FormatUint(c.slots.next, 10)
	st.pending = true
	c.slots.byToken[st.token] = key
	return ports.SpawnRequest{Argv: slices.Clone(st.argv), Env: []string{ports.SlotEnv + "=" + st.token}}
}

// placeSlotWindow puts a mapped window with a slot token into its slot. It
// returns false when the window is not a slot window (unknown token, slot
// already filled, or its workspace is gone): it then follows normal rules.
func (c *Core) placeSlotWindow(id WindowID, token string) bool {
	key, ok := c.slots.byToken[token]
	if !ok {
		return false
	}
	st := c.slots.byKey[key]
	w := c.ws.byName(key.workspace)
	if st == nil || st.window != 0 || w == nil {
		return false
	}
	st.window, st.pending = id, false
	w.AddSlotWindow(id, key.index, st.width)
	c.ws.normalize()
	return true
}

// slotWindowGone empties the slot of a closed window. The slot stays
// reserved; it is refilled when the user next shows its workspace.
func (c *Core) slotWindowGone(id WindowID) {
	for _, st := range c.slots.byKey {
		if st.window == id {
			st.window = 0
		}
	}
}

// refill returns the empty, idle slots of the workspace on screen.
func (c *Core) refill() []slotKey {
	name := c.ws.Current().Name
	if name == "" {
		return nil
	}
	var keys []slotKey
	for key, st := range c.slots.byKey {
		if key.workspace == name && st.window == 0 && !st.pending {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b slotKey) int { return a.index - b.index })
	return keys
}
