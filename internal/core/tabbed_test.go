package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// cmd+t tabs the column: the hidden tab is configured not visible, the
// visible one is, below the tab bar.
func TestTabbedHiddenTabsNotVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, nil, left)
		r.mapWindow(t, 1)
		r.mapWindow(t, 2)
		r.key(t, "bracketleft", domain.ModAlt) // stack 2 under 1
		for len(r.commands) > 0 {
			<-r.commands
		}
		r.key(t, "t", domain.ModAlt)
		synctest.Wait()
		last := map[domain.WindowID]ports.ConfigureWindow{}
		for len(r.commands) > 0 {
			if v, ok := (<-r.commands).(ports.ConfigureWindow); ok {
				last[v.ID] = v
			}
		}
		hid, ok := last[1]
		if !ok || hid.Visible {
			t.Fatalf("hidden tab configure %+v (sent %v)", hid, ok)
		}
		vis, ok := last[2]
		if !ok || !vis.Visible || vis.Height >= 100 {
			t.Fatalf("visible tab configure %+v (sent %v)", vis, ok)
		}
	})
}
