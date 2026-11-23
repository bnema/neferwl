package workspace

import (
	"slices"
	"testing"
)

func lendWorkspace(id uint64, windows ...WindowID) *Workspace {
	w := workspace()
	w.ID = id
	for _, v := range windows {
		w.AddWindow(v)
	}
	return w
}

func columnIDs(w *Workspace) []WindowID {
	var ids []WindowID
	for _, c := range w.Columns {
		ids = append(ids, c.Windows...)
	}
	return ids
}

func count(ids []WindowID, id WindowID) int {
	n := 0
	for _, v := range ids {
		if v == id {
			n++
		}
	}
	return n
}

// Lending a shared stash moves the workspace's own stashed windows into it.
func TestLendMovesOwnEntries(t *testing.T) {
	var s Stash
	s.entries = []Float{{ID: 9}}
	w := lendWorkspace(1, 1, 2)
	w.ToggleWindowStash() // 2 in own
	w.Lend(&s)
	if s.Len() != 2 || s.entries[1].ID != 2 || s.entries[1].origin != 1 || s.entries[1].back == nil {
		t.Fatalf("shared %+v", s.entries)
	}
	if len(w.own.entries) != 0 || w.StashLen() != 2 {
		t.Fatalf("own %+v, seen %d", w.own.entries, w.StashLen())
	}
	if w.Empty() {
		t.Fatal("tile 1 still on w")
	}
}

// A workspace that gives the stash back no longer sees it; the next one
// sees each window once.
func TestLendNilHidesShared(t *testing.T) {
	var s Stash
	a := lendWorkspace(1, 1, 2)
	a.Lend(&s)
	a.ToggleWindowStash() // 2 shared
	a.Lend(nil)
	if a.Has(2) || a.StashLen() != 0 || slices.Contains(a.AppendWindows(nil), 2) {
		t.Fatalf("a still sees 2: %v", a.AppendWindows(nil))
	}
	for _, p := range a.Layout() {
		if p.ID == 2 {
			t.Fatal("a lays out 2")
		}
	}
	b := lendWorkspace(2, 3)
	b.Lend(&s)
	if !b.Has(2) || count(b.AppendWindows(nil), 2) != 1 {
		t.Fatalf("b windows %v", b.AppendWindows(nil))
	}
	n := 0
	for _, p := range b.Layout() {
		if p.ID == 2 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("b lays out 2 %d times", n)
	}
}

// A fullscreen stash member follows the shared stash to the next workspace.
func TestLendFullscreenFollows(t *testing.T) {
	var s Stash
	a := lendWorkspace(1, 1, 5)
	a.Lend(&s)
	a.ToggleWindowStash() // 5 shared, shown
	a.SetFullscreen(5, true)
	if a.Fullscreen != 5 {
		t.Fatalf("setup: fullscreen %d", a.Fullscreen)
	}
	a.Lend(nil)
	if a.Fullscreen != 0 || s.fullscreen != 5 {
		t.Fatalf("a fullscreen %d, pending %d", a.Fullscreen, s.fullscreen)
	}
	b := lendWorkspace(2, 3)
	b.Lend(&s)
	if b.Fullscreen != 5 || s.fullscreen != 0 || a.Fullscreen != 0 {
		t.Fatalf("b fullscreen %d, pending %d, a %d", b.Fullscreen, s.fullscreen, a.Fullscreen)
	}
	if id, _ := b.Focused(); id != 5 {
		t.Fatalf("b focused %d", id)
	}
}

func TestStashAbsorb(t *testing.T) {
	mk := func() (Stash, Stash) {
		dst := Stash{entries: []Float{{ID: 1}, {ID: 2}}, at: 1, hidden: true}
		src := Stash{entries: []Float{{ID: 3}, {ID: 4}}, at: 1, focus: true, fullscreen: 4}
		return dst, src
	}
	t.Run("prefer", func(t *testing.T) {
		dst, src := mk()
		dst.Absorb(&src, true)
		if dst.Len() != 4 || dst.at != 3 || !dst.focus || dst.hidden || dst.fullscreen != 4 {
			t.Fatalf("dst %+v", dst)
		}
		if src.Len() != 0 || src.focus || src.fullscreen != 0 {
			t.Fatalf("src %+v", src)
		}
	})
	t.Run("keep", func(t *testing.T) {
		dst, src := mk()
		dst.Absorb(&src, false)
		// The pending fullscreen survives: dst had none.
		if dst.Len() != 4 || dst.at != 1 || dst.focus || !dst.hidden || dst.fullscreen != 4 {
			t.Fatalf("dst %+v", dst)
		}
		if src.Len() != 0 {
			t.Fatalf("src %+v", src)
		}
	})
	t.Run("prefer keeps dst pending fullscreen", func(t *testing.T) {
		dst, src := mk()
		dst.fullscreen, src.fullscreen = 2, 0
		dst.Absorb(&src, true)
		if dst.fullscreen != 2 {
			t.Fatalf("dst %+v", dst)
		}
	})
	t.Run("empty takes src", func(t *testing.T) {
		var dst Stash
		_, src := mk()
		dst.Absorb(&src, false)
		if dst.Len() != 2 || dst.at != 1 || !dst.focus || dst.fullscreen != 4 {
			t.Fatalf("dst %+v", dst)
		}
	})
}

// A window stashed on another workspace tiles right of the focused column.
func TestUnstashForeignOrigin(t *testing.T) {
	var s Stash
	a := lendWorkspace(1, 1, 2, 3)
	a.Lend(&s)
	a.FocusID(2)
	a.ToggleWindowStash() // 2 shared
	a.Lend(nil)
	b := lendWorkspace(2, 10, 11, 12)
	b.FocusID(11)
	b.Lend(&s)
	b.ToggleWindowStash() // focused stash entry 2 returns
	if got := columnIDs(b); !slices.Equal(got, []WindowID{10, 11, 2, 12}) || b.Focus != 2 {
		t.Fatalf("columns %v focus %d", got, b.Focus)
	}
}

// Stashed and unstashed on one workspace, a window returns to its place.
func TestUnstashSameOrigin(t *testing.T) {
	var s Stash
	w := lendWorkspace(1, 1, 2, 3)
	w.Lend(&s)
	w.FocusID(2)
	w.ToggleWindowStash() // 2 stashed from the middle
	w.FocusID(1)
	w.FocusID(2)
	w.ToggleWindowStash() // 2 returns
	if got := columnIDs(w); !slices.Equal(got, []WindowID{1, 2, 3}) || w.Focus != 1 {
		t.Fatalf("columns %v focus %d", got, w.Focus)
	}
}

// Giving away a stash that holds the workspace's fullscreen leaves the
// fullscreen pending in the destination.
func TestGiveStashFullscreenPending(t *testing.T) {
	w := lendWorkspace(1, 1, 5)
	w.ToggleWindowStash() // 5 in own, shown
	w.SetFullscreen(5, true)
	if w.Fullscreen != 5 {
		t.Fatalf("setup: fullscreen %d", w.Fullscreen)
	}
	var dst Stash
	w.GiveStash(&dst, false)
	if w.Fullscreen != 0 || dst.fullscreen != 5 || dst.Len() != 1 || w.StashLen() != 0 || w.Has(5) {
		t.Fatalf("fullscreen %d pending %d dst %d seen %d", w.Fullscreen, dst.fullscreen, dst.Len(), w.StashLen())
	}
}

// A shown stash lent to a workspace under a covering window shows over it.
func TestLendShownOverCover(t *testing.T) {
	var s Stash
	a := lendWorkspace(1, 1, 2)
	a.Lend(&s)
	a.ToggleWindowStash() // 2 shared, shown
	a.Lend(nil)
	b := lendWorkspace(2, 3)
	b.SetFullscreen(3, true)
	if b.Cover() != 3 {
		t.Fatalf("setup: cover %d", b.Cover())
	}
	b.Lend(&s)
	if !b.StashShown() || b.Fullscreen != 3 || s.hidden {
		t.Fatalf("shown %v fullscreen %d hidden %v", b.StashShown(), b.Fullscreen, s.hidden)
	}
	if id, _ := b.Focused(); id != 2 {
		t.Fatalf("focused %d", id)
	}
}

// TakeHome moves the matching windows in order, keeps the selection on its
// window (else the nearest), moves a pending fullscreen and returns them
// hidden.
func TestStashTakeHome(t *testing.T) {
	home := func(h string) bool { return h == "B" }
	mk := func(at int) Stash {
		return Stash{entries: []Float{{ID: 1}, {ID: 4, home: "B"}, {ID: 2}, {ID: 5, home: "B"}}, at: at, fullscreen: 5}
	}
	t.Run("selection kept", func(t *testing.T) {
		s, dst := mk(2), Stash{}
		s.TakeHome(&dst, home)
		if s.Len() != 2 || s.entries[s.at].ID != 2 || s.fullscreen != 0 {
			t.Fatalf("s %+v", s)
		}
		if dst.Len() != 2 || dst.entries[0].ID != 4 || dst.entries[1].ID != 5 || dst.entries[0].home != "" || !dst.hidden || dst.fullscreen != 5 {
			t.Fatalf("dst %+v", dst)
		}
	})
	t.Run("selection moved", func(t *testing.T) {
		s, dst := mk(3), Stash{}
		s.TakeHome(&dst, home)
		if s.at != 1 {
			t.Fatalf("at %d", s.at)
		}
	})
}
