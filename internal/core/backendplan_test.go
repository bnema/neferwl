package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestPlanBackend(t *testing.T) {
	cases := map[domain.Backend]BackendPlan{
		domain.BackendDRM: {KMS: true, RequireRenderNode: true, FixedHeads: HeadsConnectors, IsolateVirtuals: true,
			Input: InputLibinputLibseat, SessionDBus: SessionDBusAlways, LogindPowerKeys: true, PersistScales: true,
			AutoTerminal: AutoTerminalConfig, AllowTestFlags: false},
		domain.BackendHeadless: {KMS: false, RequireRenderNode: false, FixedHeads: HeadsSized, IsolateVirtuals: true,
			Input: InputScript, SessionDBus: SessionDBusFlagOnly, LogindPowerKeys: false, PersistScales: false,
			AutoTerminal: AutoTerminalConfigOrFlag, AllowTestFlags: true},
		domain.BackendServer: {KMS: false, RequireRenderNode: true, FixedHeads: HeadsNone, IsolateVirtuals: false,
			Input: InputLibinputDirect, SessionDBus: SessionDBusAlways, LogindPowerKeys: false, PersistScales: true,
			AutoTerminal: AutoTerminalOff, AllowTestFlags: false},
	}
	for b, want := range cases {
		t.Run(string(b), func(t *testing.T) { assert.Equal(t, want, PlanBackend(b)) })
	}
	assert.Equal(t, BackendPlan{}, PlanBackend("bogus"))
}
