package core

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// windowRecord is what core knows about one client surface besides its
// place in the layout. Fields fill in as wayland reports them, in any order.
type windowRecord struct {
	// client holds the app ID and PID of a mapped window, for State.
	client   ports.WindowMapped
	mappedAt time.Time
	// region is the input region; nil accepts input everywhere.
	region *ports.InputRegionChanged
	// inhibitShortcuts asks to keep compositor binds while focused;
	// idleInhibit keeps the session awake.
	inhibitShortcuts bool
	idleInhibit      bool
}

// windowRegistry owns the per-surface records. Dropping a surface removes
// everything core keeps about it in one call.
type windowRegistry struct {
	records map[WindowID]*windowRecord
}

func newWindowRegistry() windowRegistry {
	return windowRegistry{records: map[WindowID]*windowRecord{}}
}

// at returns the record of id, creating it.
func (r *windowRegistry) at(id WindowID) *windowRecord {
	rec := r.records[id]
	if rec == nil {
		rec = &windowRecord{}
		r.records[id] = rec
	}
	return rec
}

// lookup returns the record of id, or the zero record when there is none.
func (r *windowRegistry) lookup(id WindowID) windowRecord {
	if rec := r.records[id]; rec != nil {
		return *rec
	}
	return windowRecord{}
}

func (r *windowRegistry) mapped(v ports.WindowMapped, now time.Time) {
	rec := r.at(v.ID)
	rec.client, rec.mappedAt = v, now
}

// setAppID updates the app ID of a mapped window only.
func (r *windowRegistry) setAppID(id WindowID, appID string) {
	if rec := r.records[id]; rec != nil && rec.client.ID != 0 {
		rec.client.AppID = appID
	}
}

func (r *windowRegistry) setRegion(v ports.InputRegionChanged) { r.at(v.ID).region = &v }

func (r *windowRegistry) setInhibitShortcuts(id WindowID, on bool) {
	if on {
		r.at(id).inhibitShortcuts = true
	} else if rec := r.records[id]; rec != nil {
		rec.inhibitShortcuts = false
	}
}

func (r *windowRegistry) setIdleInhibit(id WindowID, on bool) {
	if on {
		r.at(id).idleInhibit = true
	} else if rec := r.records[id]; rec != nil {
		rec.idleInhibit = false
	}
}

// drop forgets the surface.
func (r *windowRegistry) drop(id WindowID) { delete(r.records, id) }

// layerGone forgets the input region of a layer surface that is no longer
// mapped; layers get no WindowUnmapped. The record goes once it is empty.
func (r *windowRegistry) layerGone(id WindowID) {
	rec := r.records[id]
	if rec == nil {
		return
	}
	rec.region = nil
	if *rec == (windowRecord{}) {
		delete(r.records, id)
	}
}

// acceptsInput reports whether the surface-local point is in id's input
// region.
func (r *windowRegistry) acceptsInput(id WindowID, x, y float64) bool {
	rec := r.records[id]
	if rec == nil || rec.region == nil || rec.region.All {
		return true
	}
	for _, box := range rec.region.Rects {
		if x >= float64(box.X) && x < float64(box.X+box.W) && y >= float64(box.Y) && y < float64(box.Y+box.H) {
			return true
		}
	}
	return false
}
