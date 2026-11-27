package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/domain"
)

// maxHomes bounds Workspace.Homes.
const maxHomes = 4

// twinKey is the EDID fingerprint shared by identical monitors.
type twinKey struct{ id, hash string }

// noteTwins remembers, for the session, every fingerprint that two connected
// physical screens share: a ref with that fingerprint then also requires its
// connector, so unplugging one twin never sends its workspaces to the other.
func (c *Core) noteTwins(s *screen) {
	if s.name() == "" || !s.info.Physical() || s.info.EDIDID == "" {
		return
	}
	k := twinKey{s.info.EDIDID, s.info.EDIDHash}
	for _, o := range c.screens {
		if o != s && o.name() != "" && o.info.Physical() && o.info.EDIDID == k.id && o.info.EDIDHash == k.hash {
			if c.twins == nil {
				c.twins = map[twinKey]bool{}
			}
			c.twins[k] = true
			return
		}
	}
}

// homeCandidate reports whether s can be a home: connected and physical.
func homeCandidate(s *screen) bool { return s.name() != "" && s.info.Physical() }

// resolveHome finds the connected screen r designates, or nil to stay put.
// An EDID identifier match wins (twins need their connector too, ambiguous
// matches narrow by hash then connector); otherwise the connector matches when
// at most one side has an identifier. It does not allocate: settleGuests runs
// it on every workspace switch.
func (c *Core) resolveHome(r domain.MonitorRef) *screen {
	if r.EDIDID != "" {
		twin := c.twins[twinKey{r.EDIDID, r.EDIDHash}]
		// Counters per narrowing step: same ID, then same hash (all of them
		// when the ref has none), then same connector among those; conns
		// counts the connector among all same-ID candidates.
		var ids, hashes, narrow, conns int
		var id, hash, last, conn *screen
		for _, s := range c.screens {
			if !homeCandidate(s) || s.info.EDIDID != r.EDIDID {
				continue
			}
			ids, id = ids+1, s
			sameConn := s.name() == r.Connector
			if sameConn {
				conns, conn = conns+1, s
			}
			if r.EDIDHash != "" && s.info.EDIDHash != r.EDIDHash {
				continue
			}
			hashes, hash = hashes+1, s
			if sameConn {
				narrow, last = narrow+1, s
			}
		}
		switch {
		case ids == 0:
			// Fall back to the connector below.
		case twin:
			if conns == 1 {
				return conn
			}
			return nil
		case ids == 1:
			return id
		case hashes == 1:
			return hash
		case narrow == 1:
			return last
		default: // ambiguous twins: stay put
			return nil
		}
	}
	for _, s := range c.screens {
		if homeCandidate(s) && s.name() == r.Connector && (r.EDIDID == "" || s.info.EDIDID == "") {
			return s
		}
	}
	return nil
}

// homeOf is the screen w belongs to: the one HomeSpec matches, else the first
// of Homes that resolves. Nil means w belongs where it is.
func (c *Core) homeOf(w *Workspace) *screen {
	s, _ := c.homeIndex(w)
	return s
}

// homeIndex is homeOf with the index of the matched ref in w.Homes, -1 when
// HomeSpec matched or nothing did.
func (c *Core) homeIndex(w *Workspace) (*screen, int) {
	if w.HomeSpec != "" {
		for _, s := range c.screens {
			if s.mon.matches(w.HomeSpec) {
				return s, -1
			}
		}
		return nil, -1
	}
	for i, r := range w.Homes {
		if s := c.resolveHome(r); s != nil {
			return s, i
		}
	}
	return nil, -1
}

// arrive records that w reached its home s through Homes[idx]: that ref
// takes s's current identity, so the connector fallback follows a monitor
// replugged elsewhere. idx < 0 means HomeSpec matched: the config home is
// reached and cleared, until named() sets it again. HomePos is consumed.
func arrive(w *Workspace, s *screen, idx int) {
	if idx < 0 {
		w.HomeSpec = ""
	} else {
		ref := s.info.Ref()
		w.Homes[idx] = ref
		// Keep Homes deduplicated: drop other entries now naming the same.
		kept := w.Homes[:0]
		for i, h := range w.Homes {
			if i == idx || h.Connector != ref.Connector || h.EDIDID != ref.EDIDID {
				kept = append(kept, h)
			}
		}
		clear(w.Homes[len(kept):])
		w.Homes = kept
	}
	w.HomePos = 0
	w.Stranded = false
}

// strand tags w, kept by the placeholder when the physical last output gone
// left, as stranded: it returns home even from the screen. An unnamed empty
// workspace is skipped.
func strand(w *Workspace, gone *screen, pos int) {
	if w.Empty() && w.Name == "" {
		return
	}
	tagHome(w, gone, pos)
	w.Stranded = true
}

// unstrandAtHome clears Stranded on workspaces already held by their home:
// the first screen back was the home itself.
func (c *Core) unstrandAtHome() {
	for _, s := range c.screens {
		for w := range s.mon.all() {
			if w.Stranded && c.homeOf(w) == s {
				w.Stranded = false
			}
		}
	}
}

// tagHome remembers the physical screen gone as home of w, at pos, unless w
// already has a home (Homes or HomeSpec); an unnamed empty workspace has none.
func tagHome(w *Workspace, gone *screen, pos int) {
	if !gone.info.Physical() || len(w.Homes) > 0 || w.HomeSpec != "" || (w.Empty() && w.Name == "") {
		return
	}
	w.Homes = append(w.Homes[:0], gone.info.Ref())
	w.HomePos = pos
}

// pushHome makes ref w's most preferred home, dropping an older entry for the
// same connector and identifier, and keeps at most maxHomes.
func pushHome(w *Workspace, ref domain.MonitorRef) {
	homes := slices.DeleteFunc(w.Homes, func(h domain.MonitorRef) bool {
		return h.Connector == ref.Connector && h.EDIDID == ref.EDIDID
	})
	homes = slices.Insert(homes, 0, ref)
	if len(homes) > maxHomes {
		clear(homes[maxHomes:])
		homes = homes[:maxHomes]
	}
	w.Homes = homes
}
