package ports

import "github.com/bnema/neferwl/internal/domain"

// Anchor edges, as in zwlr_layer_surface_v1.anchor.
const (
	AnchorTop    uint32 = 1
	AnchorBottom uint32 = 2
	AnchorLeft   uint32 = 4
	AnchorRight  uint32 = 8
)

// LayerChanged carries wayland → core the full list of mapped layer surfaces,
// sent whenever a layer surface maps, unmaps or commits new layer state.
type LayerChanged struct{ Layers []domain.LayerSurface }

func (LayerChanged) clientEvent() {}
