package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestPlanBackend(t *testing.T) {
	drm := BackendPlan{KMS: true, RequireRenderNode: true, FixedHeads: HeadsConnectors, IsolateVirtuals: true, Input: InputLibinputLibseat, SessionDBus: SessionDBusAlways, LogindPowerKeys: true, PersistScales: true, AutoTerminal: AutoTerminalConfig}
	ephemeral := BackendPlan{FixedHeads: HeadsSized, IsolateVirtuals: true, SessionDBus: SessionDBusFlagOnly, AutoTerminal: AutoTerminalConfigOrFlag}
	persistent := BackendPlan{RequireRenderNode: true, FixedHeads: HeadsNone, Input: InputLibinputDirect, SessionDBus: SessionDBusAlways, PersistScales: true, AutoTerminal: AutoTerminalOff}
	for _, b := range []domain.Backend{domain.BackendDRM, domain.BackendHeadless, "server", "bogus"} {
		for _, p := range []domain.Profile{domain.ProfileEphemeral, domain.ProfilePersistent} {
			base := BackendPlan{}
			switch b {
			case domain.BackendDRM:
				base = drm
			case domain.BackendHeadless:
				base = ephemeral
				if p == domain.ProfilePersistent {
					base = persistent
				}
			}
			if got := PlanBackend(b, p, PlanOverrides{}); got != base {
				t.Fatalf("%s/%s: %+v want %+v", b, p, got, base)
			}
			// Exercise every combination of explicit true/false overrides and inputs.
			for bits := 0; bits < 16; bits++ {
				dbus, terminal, persist := bits&1 != 0, bits&2 != 0, bits&4 != 0
				input := InputNone
				if bits&8 != 0 {
					input = InputLibinputDirect
				}
				overrides := PlanOverrides{SessionDBus: &dbus, AutoTerminal: &terminal, PersistScales: &persist, Input: &input}
				want := base
				if b == domain.BackendHeadless {
					want.SessionDBus = SessionDBusFlagOnly
					if dbus {
						want.SessionDBus = SessionDBusAlways
					}
					want.AutoTerminal = AutoTerminalOff
					if terminal {
						want.AutoTerminal = AutoTerminalConfigOrFlag
					}
					want.PersistScales = persist
					want.Input = input
				}
				if got := PlanBackend(b, p, overrides); got != want {
					t.Fatalf("%s/%s override %d: %+v want %+v", b, p, bits, got, want)
				}
			}
		}
	}
	if got := PlanBackend(domain.BackendHeadless, domain.Profile(99), PlanOverrides{}); got != (BackendPlan{}) {
		t.Fatal(got)
	}
}
