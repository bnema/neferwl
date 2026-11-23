package core

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// An output-scope workspace switch with the stash shown flashes the stash
// window it lands on, never a tile under the stash.
func TestOutputStashSwitchFlashesNoTile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashWorkspaces(t, OverflowScroll)
		c.cfg.Stash.Scope = domain.StashScopeOutput
		m := c.cur().mon
		c.windows.mapped(ports.WindowMapped{ID: 5}, clock.now.Add(-time.Minute))
		stashTile(m.Current(), 5)
		c.lendStashes()
		c.applyAction(ActionFocusWorkspacePrev)
		pending := c.flash.pending
		c.lendStashes()
		if m.Current() != m.Workspaces[0] || !m.Current().StashShown() {
			t.Fatal("setup: not on the first workspace with the stash shown")
		}
		if pending == nil || pending.window != 5 {
			t.Fatalf("flash %+v, want one on stash window 5", pending)
		}
	})
}
