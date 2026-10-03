package app

import (
	"github.com/bnema/neferwl/internal/adapters/wayland"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

// pipes are the channels between a session's goroutines. newPipes makes
// them all, with their buffer sizes; core, wayland and outputs hand each
// side the ends it uses.
type pipes struct {
	securityChanges chan ports.SecurityState
	securityEvents  chan ports.SecurityBackendEvent

	client   chan ports.ClientEvent
	input    chan ports.InputEvent
	output   chan ports.OutputEvent
	commands chan ports.ClientCommand
	spawn    chan ports.SpawnRequest

	// Capacity 1: each holds the latest value its sender published.
	scenes        chan []ports.Scene
	renderScenes  chan []ports.Scene
	layouts       chan ports.Layout
	constraints   chan ports.PointerConstraint
	states        chan ports.State
	workspaces    chan ports.Workspaces
	cursorChanges chan ports.CursorChange
	keymaps       chan *xkb.Keymap
	deviceConfigs chan ports.InputDevicesConfig

	// Configuration: watched from the file, filtered by relayConfig, then
	// configured (overrides included) by outputs for core.
	watched      chan ports.ConfigChanged
	filtered     chan ports.ConfigChanged
	configured   chan ports.ConfigChanged
	configErrors chan error
	// scales is nil outside a real session: headless runs never touch the
	// config file.
	scales chan ports.ScaleChanged

	contents  chan ports.SurfaceContent
	presented chan ports.OutputPresented
	// Outputs report on flips; relayPresented passes them to wayland
	// (presented) and tells core about each flip (frames) to move running
	// slides.
	flips  chan ports.OutputPresented
	frames chan ports.OutputFrame

	captures chan ports.CaptureRequest
	// Every admitted capture owns one reply slot until wayland consumes it.
	// Keep this capacity tied to admission so output routing never waits.
	captured chan ports.CaptureDone

	outputFormats chan ports.OutputFormats
	outputHeads   chan ports.OutputHeads
	leaseRequests chan ports.LeaseMessage
	leaseEvents   chan ports.LeaseMessage
	applyOutput   chan ports.OutputApply
	appliedOutput chan ports.OutputApplied

	// D-Bus idle inhibition and SimulateUserActivity (screensaver), for
	// wayland's idle notifications. Activity sends never block: capacity 1
	// coalesces them.
	idleInhibited chan bool
	idleActivity  chan struct{}
}

func newPipes(realSession bool) *pipes {
	p := &pipes{
		securityChanges: make(chan ports.SecurityState, 8),
		securityEvents:  make(chan ports.SecurityBackendEvent, 64),

		client:   make(chan ports.ClientEvent, 32),
		input:    make(chan ports.InputEvent, 32),
		output:   make(chan ports.OutputEvent, 32),
		commands: make(chan ports.ClientCommand, 32),
		spawn:    make(chan ports.SpawnRequest, 32),

		scenes:        make(chan []ports.Scene, 1),
		renderScenes:  make(chan []ports.Scene, 1),
		layouts:       make(chan ports.Layout, 1),
		constraints:   make(chan ports.PointerConstraint, 1),
		states:        make(chan ports.State, 1),
		workspaces:    make(chan ports.Workspaces, 1),
		cursorChanges: make(chan ports.CursorChange, 1),
		keymaps:       make(chan *xkb.Keymap, 1),
		deviceConfigs: make(chan ports.InputDevicesConfig, 1),

		watched:      make(chan ports.ConfigChanged, 8),
		filtered:     make(chan ports.ConfigChanged, 8),
		configured:   make(chan ports.ConfigChanged, 8),
		configErrors: make(chan error, 8),

		contents:  make(chan ports.SurfaceContent, 64),
		presented: make(chan ports.OutputPresented, 64),
		flips:     make(chan ports.OutputPresented, 64),
		frames:    make(chan ports.OutputFrame, 8),

		captures: make(chan ports.CaptureRequest, ports.MaxCaptureInflight),
		captured: make(chan ports.CaptureDone, ports.MaxCaptureInflight),

		outputFormats: make(chan ports.OutputFormats, 8),
		outputHeads:   make(chan ports.OutputHeads, 8),
		leaseRequests: make(chan ports.LeaseMessage, 32),
		leaseEvents:   make(chan ports.LeaseMessage, 32),
		applyOutput:   make(chan ports.OutputApply, 8),
		appliedOutput: make(chan ports.OutputApplied, 8),

		idleInhibited: make(chan bool, 1),
		idleActivity:  make(chan struct{}, 1),
	}
	if realSession {
		p.scales = make(chan ports.ScaleChanged, 8)
	}
	return p
}

// core is core's side. Security, Terminal and Clock are not channels: the
// caller sets them.
func (p *pipes) core() core.Channels {
	return core.Channels{
		Client:       p.client,
		Input:        p.input,
		Output:       p.output,
		Config:       p.configured,
		Commands:     p.commands,
		Spawn:        p.spawn,
		Scenes:       p.scenes,
		Layouts:      p.layouts,
		Constraints:  p.constraints,
		State:        p.states,
		Workspaces:   p.workspaces,
		ConfigErrors: p.configErrors,
		Scales:       p.scales,
		Frames:       p.frames,
	}
}

// wayland is the wayland server's side.
func (p *pipes) wayland() wayland.Channels {
	return wayland.Channels{
		SecurityChanges: p.securityChanges,
		SecurityEvents:  p.securityEvents,
		Events:          p.client,
		Commands:        p.commands,
		Workspaces:      p.workspaces,
		Contents:        p.contents,
		Cursors:         p.cursorChanges,
		Presented:       p.presented,
		Captures:        p.captures,
		Captured:        p.captured,
		OutputFormats:   p.outputFormats,
		OutputHeads:     p.outputHeads,
		LeaseRequests:   p.leaseRequests,
		LeaseEvents:     p.leaseEvents,
		OutputApply:     p.applyOutput,
		OutputApplied:   p.appliedOutput,
		IdleInhibited:   p.idleInhibited,
		IdleActivity:    p.idleActivity,
	}
}

// outputs is the outputs' side (DRM or headless).
func (p *pipes) outputs(security ports.SessionSecurity) outputChannels {
	return outputChannels{
		security:        security,
		securityChanges: p.securityChanges,
		securityEvents:  p.securityEvents,
		events:          p.output,
		scenes:          p.renderScenes,
		contents:        p.contents,
		cursorChanges:   p.cursorChanges,
		presented:       p.flips,
		captures:        p.captures,
		captured:        p.captured,
		formats:         p.outputFormats,
		heads:           p.outputHeads,
		leaseRequests:   p.leaseRequests,
		leaseEvents:     p.leaseEvents,
		reloads:         p.filtered,
		configured:      p.configured,
		requests:        p.applyOutput,
		replies:         p.appliedOutput,
	}
}
