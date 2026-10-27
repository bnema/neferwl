package workspace

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
)

// The zoom does not depend on the number of bands.
func TestOverviewCascadeZoomIgnoresBandCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if one, three := cascadeWorkspace(3).overviewZoom(layout.Horizontal), cascadeWorkspace(7).overviewZoom(layout.Horizontal); one != three || one != overviewMaxZoom {
			t.Fatalf("zoom with one band %v, with three %v", one, three)
		}

	})
}
