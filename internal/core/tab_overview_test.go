package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// A hidden tab keeps its configure across an overview open and close.
func TestTabbedOverviewKeepsHiddenTabConfigure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, func(c *domain.Config) { c.Binds["F2"] = "toggle-overview" }, left)
		r.mapWindow(t, 1)
		r.mapWindow(t, 2)
		r.key(t, "bracketleft", domain.ModAlt)
		r.key(t, "t", domain.ModAlt)
		synctest.Wait()
		for len(r.commands) > 0 {
			<-r.commands
		}
		r.key(t, "F2", 0)
		synctest.Wait()
		r.key(t, "F2", 0)
		synctest.Wait()
		for len(r.commands) > 0 {
			if v, ok := (<-r.commands).(ports.ConfigureWindow); ok && v.Width == 0 && v.Height == 0 {
				t.Fatalf("zero-size configure %+v", v)
			}
		}
	})
}
