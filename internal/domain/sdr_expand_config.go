package domain

import "regexp"

// SDRExpandMode is per-output policy. Zero is auto, including unset config.
type SDRExpandMode uint8

const (
	SDRExpandAuto SDRExpandMode = iota
	SDRExpandOff
	SDRExpandAlways
)

func (m SDRExpandMode) String() string {
	switch m {
	case SDRExpandAuto:
		return "auto"
	case SDRExpandOff:
		return "off"
	case SDRExpandAlways:
		return "always"
	default:
		return "unknown"
	}
}

func ParseSDRExpandMode(s string) (SDRExpandMode, bool) {
	switch s {
	case "auto":
		return SDRExpandAuto, true
	case "off":
		return SDRExpandOff, true
	case "always":
		return SDRExpandAlways, true
	default:
		return SDRExpandAuto, false
	}
}

// SDRExpandRule overrides window eligibility, never output HDR/headroom gates.
// Game classifies an otherwise unmarked window as a game for auto policy.
type SDRExpandRule uint8

const (
	SDRExpandGame SDRExpandRule = iota
	SDRExpandOn
	SDRExpandForceOff
)

func (r SDRExpandRule) String() string {
	switch r {
	case SDRExpandGame:
		return "game"
	case SDRExpandOn:
		return "on"
	case SDRExpandForceOff:
		return "off"
	default:
		return "unknown"
	}
}

func ParseSDRExpandRule(s string) (SDRExpandRule, bool) {
	switch s {
	case "game":
		return SDRExpandGame, true
	case "on":
		return SDRExpandOn, true
	case "off":
		return SDRExpandForceOff, true
	default:
		return SDRExpandGame, false
	}
}

const DefaultSDRExpandIntensity = 0.5

// DefaultWindowRules precede user rules. Steam X11 games lack Wayland content
// type metadata; this classifies them without bypassing eligibility gates.
func DefaultWindowRules() []WindowRule {
	game := SDRExpandGame
	return []WindowRule{{Name: "builtin-steam-game", AppID: regexp.MustCompile(`^steam_app_\d+$`), SDRExpand: &game}}
}
