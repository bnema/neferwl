package core

import "github.com/bnema/neferwl/internal/domain"

// HeadsSource tells where a backend's fixed output heads come from.
type HeadsSource uint8

const (
	HeadsNone       HeadsSource = iota // no fixed heads
	HeadsConnectors                    // DRM connectors
	HeadsSized                         // headless heads of a configured size
)

// InputSource tells how a backend receives input.
type InputSource uint8

const (
	InputNone            InputSource = iota
	InputLibinputLibseat             // libinput with devices opened through libseat
	InputScript                      // scripted input (headless)
	InputLibinputDirect              // libinput opening devices directly
)

// SessionDBusMode tells when the session D-Bus services start.
type SessionDBusMode uint8

const (
	SessionDBusFlagOnly SessionDBusMode = iota // only when requested by flag
	SessionDBusAlways                          // always
)

// AutoTerminalMode tells when a terminal is spawned at startup.
type AutoTerminalMode uint8

const (
	AutoTerminalOff          AutoTerminalMode = iota // never
	AutoTerminalConfig                               // per config
	AutoTerminalConfigOrFlag                         // per config, overridable by flag
)

// BackendPlan is the per-backend decision table consumed by wiring.
type BackendPlan struct {
	KMS               bool
	RequireRenderNode bool // false lets headless fall back to any Vulkan device
	FixedHeads        HeadsSource
	IsolateVirtuals   bool
	Input             InputSource
	SessionDBus       SessionDBusMode
	LogindPowerKeys   bool
	PersistScales     bool
	AutoTerminal      AutoTerminalMode
	AllowTestFlags    bool
}

// PlanBackend returns the plan for b; an unknown backend yields the zero plan.
func PlanBackend(b domain.Backend) BackendPlan {
	switch b {
	case domain.BackendDRM:
		return BackendPlan{KMS: true, RequireRenderNode: true, FixedHeads: HeadsConnectors, IsolateVirtuals: true,
			Input: InputLibinputLibseat, SessionDBus: SessionDBusAlways, LogindPowerKeys: true, PersistScales: true,
			AutoTerminal: AutoTerminalConfig}
	case domain.BackendHeadless:
		return BackendPlan{FixedHeads: HeadsSized, IsolateVirtuals: true, Input: InputScript,
			SessionDBus: SessionDBusFlagOnly, AutoTerminal: AutoTerminalConfigOrFlag, AllowTestFlags: true}
	case domain.BackendServer:
		return BackendPlan{RequireRenderNode: true, FixedHeads: HeadsNone, Input: InputLibinputDirect,
			SessionDBus: SessionDBusFlagOnly, PersistScales: true, AutoTerminal: AutoTerminalOff}
	}
	return BackendPlan{}
}
