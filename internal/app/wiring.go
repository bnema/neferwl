package app

import (
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/wayland"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// pipes are the channels between a session's goroutines. newPipes makes
// them all, with their buffer sizes; core, wayland and outputs hand each
// side the ends it uses.
type pipes struct {
	securityChanges chan domain.SecurityState
	securityEvents  chan ports.SecurityBackendEvent

	client   chan ports.ClientEvent
	input    chan ports.InputEvent
	output   chan ports.OutputEvent
	commands chan ports.ClientCommand
	spawn    chan domain.SpawnRequest

	// Capacity 1: each holds the latest value its sender published.
	scenes        chan []domain.Scene
	renderScenes  chan []domain.Scene
	layouts       chan domain.Layout
	constraints   chan domain.PointerConstraint
	states        chan domain.State
	workspaces    chan domain.Workspaces
	cursorChanges chan domain.CursorChange
	keymaps       chan *xkb.Keymap
	deviceConfigs chan domain.InputDevicesConfig

	// Configuration: watched from the file, filtered by relayConfig, then
	// configured (overrides included) by outputs for core.
	watched      chan domain.ConfigChanged
	filtered     chan domain.ConfigChanged
	configured   chan domain.ConfigChanged
	configErrors chan error
	// scales is nil outside a real session: headless runs never touch the
	// config file.
	scales chan domain.ScaleChanged
	// binds carries bind dispatch records to the debug log.
	binds chan domain.InputTrace

	// Outputs report to wayland through its presentation sink and, for each
	// flip of an animating scene (Scene.Animating), tell core (frames) to
	// move running slides.
	frames chan domain.OutputFrame

	// Hint mode: core asks the hints adapter for a scan and gets the result.
	hintScans   chan domain.HintScanRequest
	hintResults chan domain.HintScanResult

	captures chan domain.CaptureRequest
	// Every admitted capture owns one reply slot until routeCaptured takes
	// it. Keep this capacity tied to admission so output routing never waits.
	// With hint mode on, the outputs reply on capturedAll and routeCaptured splits the replies by
	// ports.CaptureIDInternal into captured (the wayland consumer) and
	// hintCaptured (internal consumers). With hint mode off, capturedAll is
	// captured itself and there is no routeCaptured.
	capturedAll chan domain.CaptureDone
	captured    chan domain.CaptureDone
	// hintCaptured carries internal capture replies to the hints adapter.
	hintCaptured chan domain.CaptureDone

	outputFormats chan domain.OutputFormats
	outputHeads   chan domain.OutputHeads
	leaseRequests chan ports.LeaseMessage
	leaseEvents   chan ports.LeaseMessage
	applyOutput   chan domain.OutputApply
	appliedOutput chan domain.OutputApplied

	// D-Bus idle inhibition and SimulateUserActivity (screensaver), for
	// wayland's idle notifications. Activity sends never block: capacity 1
	// coalesces them.
	idleInhibited chan bool
	idleActivity  chan struct{}

	// Screencast commands to wayland (the screencast adapter's side is the
	// sender); the events come back through wayland.Options.Casts.
	castCommands chan domain.CastCommand
}

// newPipes makes the channels. The hint channels exist only with hint set
// (hint.enable = on): without them core's hint action is a no-op and nothing
// of hint mode is allocated.
func newPipes(realSession, hint bool) *pipes {
	p := &pipes{
		securityChanges: make(chan domain.SecurityState, 8),
		securityEvents:  make(chan ports.SecurityBackendEvent, 64),

		client:   make(chan ports.ClientEvent, 32),
		input:    make(chan ports.InputEvent, 32),
		output:   make(chan ports.OutputEvent, 32),
		commands: make(chan ports.ClientCommand, 32),
		spawn:    make(chan domain.SpawnRequest, 32),

		scenes:        make(chan []domain.Scene, 1),
		renderScenes:  make(chan []domain.Scene, 1),
		layouts:       make(chan domain.Layout, 1),
		constraints:   make(chan domain.PointerConstraint, 1),
		states:        make(chan domain.State, 1),
		workspaces:    make(chan domain.Workspaces, 1),
		cursorChanges: make(chan domain.CursorChange, 1),
		keymaps:       make(chan *xkb.Keymap, 1),
		deviceConfigs: make(chan domain.InputDevicesConfig, 1),

		watched:      make(chan domain.ConfigChanged, 8),
		filtered:     make(chan domain.ConfigChanged, 8),
		configured:   make(chan domain.ConfigChanged, 8),
		configErrors: make(chan error, 8),

		frames: make(chan domain.OutputFrame, 8),
		binds:  make(chan domain.InputTrace, 32),

		captures: make(chan domain.CaptureRequest, ports.MaxCaptureInflight),
		captured: make(chan domain.CaptureDone, ports.MaxCaptureInflight),

		outputFormats: make(chan domain.OutputFormats, 8),
		outputHeads:   make(chan domain.OutputHeads, 8),
		leaseRequests: make(chan ports.LeaseMessage, 32),
		leaseEvents:   make(chan ports.LeaseMessage, 32),
		applyOutput:   make(chan domain.OutputApply, 8),
		appliedOutput: make(chan domain.OutputApplied, 8),

		idleInhibited: make(chan bool, 1),
		idleActivity:  make(chan struct{}, 1),

		castCommands: make(chan domain.CastCommand, 64),
	}
	// Without hint mode every reply is for the wayland consumer: the outputs
	// reply on its channel directly and nothing demultiplexes.
	p.capturedAll = p.captured
	if hint {
		p.capturedAll = make(chan domain.CaptureDone, ports.MaxCaptureInflight)
		p.hintScans = make(chan domain.HintScanRequest, 1)
		p.hintResults = make(chan domain.HintScanResult, 1)
		p.hintCaptured = make(chan domain.CaptureDone, 1)
	}
	if realSession {
		p.scales = make(chan domain.ScaleChanged, 8)
	}
	return p
}

// core is core's side: channels only. The security gate, clock and terminal
// flag are not channels; assembleSession passes them as core.Options.
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
		Binds:        p.binds,
		Frames:       p.frames,
		HintScans:    p.hintScans,
		Hints:        p.hintResults,
	}
}

// internalCaptured is the channel internal capture replies are routed to: the
// hints adapter reads it.
func (p *pipes) internalCaptured() chan<- domain.CaptureDone { return p.hintCaptured }

// wayland is the wayland server's side.
func (p *pipes) wayland() wayland.Channels {
	return wayland.Channels{
		SecurityChanges: p.securityChanges,
		SecurityEvents:  p.securityEvents,
		Events:          p.client,
		Commands:        p.commands,
		Workspaces:      p.workspaces,
		Cursors:         p.cursorChanges,
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
		CastCommands:    p.castCommands,
	}
}

// outputs is the outputs' side (DRM or headless).
func (p *pipes) outputs(security ports.SessionSecurity, presented ports.PresentationSink, content routing.ContentAttacher) routing.Channels {
	return routing.Channels{
		Security:        security,
		SecurityChanges: p.securityChanges,
		SecurityEvents:  p.securityEvents,
		Events:          p.output,
		Scenes:          p.renderScenes,
		CursorChanges:   p.cursorChanges,
		Presented:       presented,
		Content:         content,
		Frames:          p.frames,
		Captures:        p.captures,
		Captured:        p.capturedAll,
		Formats:         p.outputFormats,
		Heads:           p.outputHeads,
		LeaseRequests:   p.leaseRequests,
		LeaseEvents:     p.leaseEvents,
		Reloads:         p.filtered,
		Configured:      p.configured,
		Requests:        p.applyOutput,
		Replies:         p.appliedOutput,
	}
}
