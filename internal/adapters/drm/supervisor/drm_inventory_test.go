package supervisor

import (
	"errors"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestRetainedHeadsOnCardReadError(t *testing.T) {
	first := []domain.OutputHead{{Info: domain.OutputInfo{Name: "DP-1"}, Enabled: true}}
	other := []domain.OutputHead{{Info: domain.OutputInfo{Name: "HDMI-1"}, Enabled: true}}
	cached := retainedHeads(nil, first, nil)
	cached = retainedHeads(cached, nil, errors.New("resources unavailable"))
	combined := append(append([]domain.OutputHead{}, cached...), other...)
	if len(combined) != 2 || combined[0].Info.Name != "DP-1" || combined[1].Info.Name != "HDMI-1" {
		t.Fatalf("failed card lost its inventory: %+v", combined)
	}
	if got := retainedHeads(cached, nil, nil); len(got) != 0 {
		t.Fatalf("successful empty inventory should clear cache: %+v", got)
	}
}

func TestReleasedHeadOnInventoryError(t *testing.T) {
	mode := domain.OutputMode{Width: 1920, Height: 1080}
	previous := []domain.OutputHead{{Info: domain.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode}, {Info: domain.OutputInfo{Name: "HDMI-1"}, Enabled: true, Current: &mode}}
	// Both a disabled stop and an unsuccessful restart release the old instance.
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "restart"}[restart], func(t *testing.T) {
			heads := releasedHead(append([]domain.OutputHead(nil), previous...), "DP-1")
			heads = retainedHeads(heads, nil, errors.New("inventory unavailable"))
			if heads[0].Enabled || heads[0].Current != nil || !heads[1].Enabled {
				t.Fatalf("released connector inventory: %+v", heads)
			}
		})
	}
}
