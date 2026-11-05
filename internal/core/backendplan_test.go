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
			for bits := 0; bits < 8; bits++ {
				dbus, terminal := bits&1 != 0, bits&2 != 0
				input := InputNone
				if bits&4 != 0 {
					input = InputLibinputDirect
				}
				overrides := PlanOverrides{SessionDBus: &dbus, AutoTerminal: &terminal, Input: &input}
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
					want.Input = input
				}
				if b == domain.BackendDRM && !terminal {
					want.AutoTerminal = AutoTerminalOff
				}
				if got := PlanBackend(b, p, overrides); got != want {
					t.Fatalf("%s/%s override %d: %+v want %+v", b, p, bits, got, want)
				}
			}
		}
	}
	// Explicit sizes give headless sized heads in every profile; DRM keeps
	// its connectors.
	for _, p := range []domain.Profile{domain.ProfileEphemeral, domain.ProfilePersistent} {
		want := PlanBackend(domain.BackendHeadless, p, PlanOverrides{})
		want.FixedHeads = HeadsSized
		if got := PlanBackend(domain.BackendHeadless, p, PlanOverrides{SizedHeads: true}); got != want {
			t.Fatalf("sized %s: %+v want %+v", p, got, want)
		}
	}
	if got := PlanBackend(domain.BackendDRM, domain.ProfileEphemeral, PlanOverrides{SizedHeads: true}); got != drm {
		t.Fatalf("sized drm: %+v", got)
	}
	if got := PlanBackend(domain.BackendHeadless, domain.Profile(99), PlanOverrides{}); got != (BackendPlan{}) {
		t.Fatal(got)
	}
}
