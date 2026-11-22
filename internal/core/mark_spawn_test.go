package core_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

const markMods = domain.ModAlt | domain.ModCtrl | domain.ModShift

// markSpawnRig binds Alt+Ctrl+Shift+m to "spawn --mark a foot" and
// Alt+Ctrl+Shift+n to "stash --mark a foot".
func markSpawnRig(t *testing.T) *multiRig {
	t.Helper()
	return stashSpawnRig(t, func(c *domain.Config) {
		c.Binds["Alt+Ctrl+Shift+m"] = "spawn --mark a foot"
		c.Binds["Alt+Ctrl+Shift+n"] = "stash --mark a foot"
	})
}

// mapSlot maps id with token and returns its state.
func (r *multiRig) mapSlot(t *testing.T, id domain.WindowID, token string) domain.WindowState {
	t.Helper()
	r.client <- ports.WindowMapped{ID: id, Slot: token}
	return *stWindow(r.stateWith(t, id), id)
}

// The first window of a marked spawn gets the mark and opens on the
// focused workspace; the token marks no second window.
func TestSpawnMarkMarksFirstWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := markSpawnRig(t)
		r.mapWindow(t, 1)
		r.key(t, "m", markMods)
		token := slotToken(t, receive(t, r.spawn))
		w := r.mapSlot(t, 2, token)
		if w.Marks != "a" || w.Workspace != 1 || w.Floating || !w.Visible {
			t.Fatalf("marked window %+v", w)
		}
		r.key(t, "m", markMods) // a new claim moves nothing until it maps
		<-r.spawn
		r.client <- ports.WindowUnmapped{ID: 2}
		if w := r.mapSlot(t, 3, token); w.Marks != "" {
			t.Fatalf("token reused: %+v", w)
		}
	})
}

// A marked stash spawn opens its window stashed and marked.
func TestStashMarkMarksStashedWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := markSpawnRig(t)
		r.mapWindow(t, 1)
		r.key(t, "n", markMods)
		token := slotToken(t, receive(t, r.spawn))
		if w := r.mapSlot(t, 2, token); w.Marks != "a" || w.StashIndex == 0 {
			t.Fatalf("stash-marked window %+v", w)
		}
	})
}

// A mark claim lasts markRetry (60 s), marked stash claims included, while
// an unmarked stash claim lasts termRetry (5 s).
func TestMarkClaimExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := markSpawnRig(t)
		r.mapWindow(t, 1)
		r.key(t, "n", markMods)
		stash := slotToken(t, receive(t, r.spawn))
		r.key(t, "m", markMods)
		spawn := slotToken(t, receive(t, r.spawn))
		time.Sleep(6 * time.Second)
		if w := r.mapSlot(t, 2, stash); w.Marks != "a" || w.StashIndex == 0 {
			t.Fatalf("after 6 s %+v", w)
		}
		time.Sleep(55 * time.Second)
		if w := r.mapSlot(t, 3, spawn); w.Marks != "" {
			t.Fatalf("after 61 s %+v", w)
		}
	})
}
