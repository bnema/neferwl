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
}

// PlanOverrides holds explicit CLI choices. Nil preserves profile defaults.
type PlanOverrides struct {
	SessionDBus  *bool
	AutoTerminal *bool
	Input        *InputSource
	// SizedHeads: explicit output sizes were given (--size). Headless then
	// runs sized heads whatever the profile; DRM uses its connectors.
	SizedHeads bool
}

// PlanBackend returns the backend/profile policy consumed by wiring. DRM
// ignores profiles and overrides, except an explicit AutoTerminal=false
// (--no-terminal); its session/device ownership is fixed.
func PlanBackend(b domain.Backend, p domain.Profile, o PlanOverrides) BackendPlan {
	if b == domain.BackendDRM {
		plan := BackendPlan{KMS: true, RequireRenderNode: true, FixedHeads: HeadsConnectors, IsolateVirtuals: true,
			Input: InputLibinputLibseat, SessionDBus: SessionDBusAlways, LogindPowerKeys: true, PersistScales: true, AutoTerminal: AutoTerminalConfig}
		if o.AutoTerminal != nil && !*o.AutoTerminal {
			plan.AutoTerminal = AutoTerminalOff
		}
		return plan
	}
	if b != domain.BackendHeadless {
		return BackendPlan{}
	}
	var plan BackendPlan
	switch p {
	case domain.ProfileEphemeral:
		plan = BackendPlan{FixedHeads: HeadsSized, IsolateVirtuals: true, Input: InputNone, SessionDBus: SessionDBusFlagOnly, AutoTerminal: AutoTerminalConfigOrFlag}
	case domain.ProfilePersistent:
		plan = BackendPlan{RequireRenderNode: true, FixedHeads: HeadsNone, Input: InputLibinputDirect, SessionDBus: SessionDBusAlways, PersistScales: true, AutoTerminal: AutoTerminalOff}
	default:
		return BackendPlan{}
	}
	if o.SessionDBus != nil {
		plan.SessionDBus = SessionDBusFlagOnly
		if *o.SessionDBus {
			plan.SessionDBus = SessionDBusAlways
		}
	}
	if o.AutoTerminal != nil {
		plan.AutoTerminal = AutoTerminalOff
		if *o.AutoTerminal {
			plan.AutoTerminal = AutoTerminalConfigOrFlag
		}
	}
	if o.Input != nil {
		plan.Input = *o.Input
	}
	if o.SizedHeads {
		plan.FixedHeads = HeadsSized
	}
	return plan
}
