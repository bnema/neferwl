package atspi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/zerobus"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

const (
	// callTimeout is how long a batch waits for the next reply.
	callTimeout = 100 * time.Millisecond
	// budget is the whole time Targets may take.
	budget = 300 * time.Millisecond
)

// connectBudget bounds the whole connection setup (session bus,
// GetAddress, IsEnabled, dial of the accessibility bus) of one attempt.
const connectBudget = 300 * time.Millisecond

// retryEvery is the minimum time between two connection attempts.
const retryEvery = 5 * time.Second

// connector opens a transport on the accessibility bus.
type connector interface {
	Connect(ctx context.Context) (transport, error)
}

// stallWarnAfter is how long an attempt may be in flight before it is
// reported as a stalled accessibility bus.
const stallWarnAfter = 5 * time.Second

// dialResult is the outcome of a connection attempt.
type dialResult struct {
	tr  transport
	err error
	at  time.Time // when the attempt finished
}

// Source reads hint targets from the accessibility tree. It owns one
// connection to the accessibility bus. It is NOT safe for concurrent use:
// it is owned by the hints worker goroutine, which calls Targets and,
// when it returns, Close. The connection is made on the first Targets call
// that needs it, never at startup, and made again after a failure, at most
// once every retryEvery; meanwhile Targets finds nothing and OCR takes over.
//
// At most one connection attempt (one goroutine and one socket) exists at
// any time: an attempt that outlives the wait of ensure stays in flight, is
// polled by the later Targets calls and is never doubled.
type Source struct {
	tr   transport
	dial connector
	// attempt delivers the outcome of the attempt in flight (buffered, so
	// the attempt goroutine never blocks); nil when none runs.
	attempt        chan dialResult
	attemptStarted time.Time
	warnedStall    bool
	retryAt        time.Time
	closed         bool
	// wait is how long ensure waits for a new attempt; connectBudget if 0.
	wait time.Duration
}

// New returns a Source. It does not touch the bus: the connection is made by
// the first Targets call.
func New() *Source { return &Source{dial: busConnector{}} }

// busConnector connects to the session bus, enables accessibility and dials
// the accessibility bus.
type busConnector struct{}

func (busConnector) Connect(ctx context.Context) (transport, error) {
	log := logging.For(ctx, "atspi")
	ctx, cancel := context.WithTimeout(ctx, connectBudget)
	defer cancel()
	sess, err := zerobus.SessionBusContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("atspi: session bus: %w", err)
	}
	defer func() { _ = sess.Close() }()
	// Call has no deadline: closing the connection ends the wait for a
	// silent service.
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stop()

	sess.NewCall(a11yBusName, a11yBusPath, a11yBusName, "GetAddress", "")
	m, err := sess.Call()
	if err != nil {
		return nil, fmt.Errorf("atspi: org.a11y.Bus GetAddress: %w", err)
	}
	r := m.Body()
	addr := r.Str()
	if r.Err() != nil || addr == "" {
		return nil, errors.New("atspi: no accessibility bus address")
	}
	addr = strings.Clone(addr)

	e := sess.NewCall(a11yBusName, a11yBusPath, ifaceProps, "Set", "ssv")
	e.Str(a11yStatus)
	e.Str("IsEnabled")
	e.Variant("b")
	e.Bool(true)
	if _, err := sess.Call(); err != nil {
		// Toolkits that already see the bus keep working.
		log.Warn().Err(err).Msg("could not enable accessibility")
	} else {
		log.Info().Msg("accessibility enabled")
	}

	zc, err := zerobus.DialContext(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("atspi: accessibility bus: %w", err)
	}
	tr, err := newConn(zc)
	if err != nil {
		return nil, err
	}
	log.Info().Msg("accessibility bus connected")
	return tr, nil
}

// ensure returns the connection, making one if none exists and the last
// attempt is old enough. It waits at most the budget for a new attempt; an
// attempt that does not answer in time stays in flight (abandoned, never
// doubled) and is polled without blocking by the next calls.
func (s *Source) ensure(ctx context.Context) transport {
	if s.closed {
		return nil
	}
	if s.tr != nil {
		return s.tr
	}
	if s.dial == nil {
		return nil
	}
	now := time.Now()
	if s.attempt != nil {
		select {
		case r := <-s.attempt:
			s.finish(ctx, r)
		default:
			if !s.warnedStall && now.Sub(s.attemptStarted) > stallWarnAfter {
				s.warnedStall = true
				log := logging.For(ctx, "atspi")
				log.Warn().Dur("for", now.Sub(s.attemptStarted)).Msg("accessibility bus not answering")
			}
		}
		return s.tr
	}
	if now.Before(s.retryAt) {
		return nil
	}
	ch := make(chan dialResult, 1)
	s.attempt = ch
	s.attemptStarted = now
	s.warnedStall = false
	dial := s.dial
	go func() {
		tr, err := dial.Connect(ctx)
		ch <- dialResult{tr: tr, err: err, at: time.Now()}
	}()
	wait := s.wait
	if wait <= 0 {
		wait = connectBudget
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-ch:
		s.finish(ctx, r)
	case <-timer.C:
		log := logging.For(ctx, "atspi")
		log.Debug().Dur("after", wait).Msg("accessibility connect attempt abandoned in flight")
	case <-ctx.Done():
	}
	return s.tr
}

// finish takes the outcome of the attempt in flight: a transport is kept,
// an error starts the backoff from the time the attempt finished.
func (s *Source) finish(ctx context.Context, r dialResult) {
	s.attempt = nil
	if r.err != nil || r.tr == nil {
		s.retryAt = r.at.Add(retryEvery)
		err := r.err
		if err == nil {
			err = errors.New("atspi: no transport")
		}
		log := logging.For(ctx, "atspi")
		log.Warn().Err(err).Msg("accessibility hint targets unavailable, using OCR only")
		return
	}
	s.tr = r.tr
}

// drop closes a broken connection; the next attempt reconnects.
func (s *Source) drop() {
	s.retryAt = time.Now().Add(retryEvery)
	if s.tr != nil {
		_ = s.tr.Close()
		s.tr = nil
	}
}

// Close closes the accessibility bus connection, and the one an attempt
// still in flight may deliver late. After Close, Targets finds nothing and
// never connects. Like Targets it is called by the owner goroutine only.
func (s *Source) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.tr != nil {
		err = s.tr.Close()
		s.tr = nil
	}
	if ch := s.attempt; ch != nil {
		s.attempt = nil
		// One reaper at most: Close runs once, and one attempt exists.
		go func() {
			if r := <-ch; r.tr != nil {
				_ = r.tr.Close()
			}
		}()
	}
	return err
}

// appInfo is an application of the registry whose process shows a window.
type appInfo struct {
	root ref
	pid  int
}

// winMatch is a window with the frame it was paired with.
type winMatch struct {
	win   ports.HintWindow
	frame ref
	fx    int32
	fy    int32
}

// Targets returns clickable targets, clipped to each window unit's region,
// that are ready within the budget. Coverage is decided by the caller.
func (s *Source) Targets(ctx context.Context, windows []ports.HintWindow, linksOnly bool) ([]ports.HintTarget, error) {
	deadline := time.Now().Add(budget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	wanted := map[int]bool{}
	for _, w := range windows {
		if w.PID > 0 {
			wanted[w.PID] = true
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	tr := s.ensure(ctx)
	if tr == nil {
		return nil, nil
	}
	broken := false
	do := func(calls []call) []reply {
		if len(calls) == 0 || ctx.Err() != nil {
			return make([]reply, len(calls))
		}
		if broken {
			out := make([]reply, len(calls))
			for i := range out {
				out[i].err = ErrBroken
			}
			return out
		}
		out := tr.Do(deadline, callTimeout, calls)
		for _, r := range out {
			if errors.Is(r.err, ErrBroken) {
				broken = true
				s.drop()
				break
			}
		}
		return out
	}

	// Applications of the registry, kept when their process shows a window.
	rep := do([]call{{op: opChildren, name: registryName, path: registryRoot}})
	if len(rep) == 0 || rep[0].err != nil {
		if len(rep) > 0 {
			return nil, fmt.Errorf("atspi: registry: %w", rep[0].err)
		}
		return nil, nil
	}
	apps := rep[0].refs
	calls := make([]call, len(apps))
	for i, a := range apps {
		calls[i] = call{op: opPID, name: a.name}
	}
	var kept []appInfo
	for i, r := range do(calls) {
		if r.err == nil && wanted[int(r.u)] {
			kept = append(kept, appInfo{root: apps[i], pid: int(r.u)})
		}
	}

	// The showing windows of each kept app.
	calls = calls[:0]
	for _, a := range kept {
		calls = append(calls, call{op: opChildren, name: a.root.name, path: a.root.path})
	}
	var cands []ref
	var candApp []int
	for i, r := range do(calls) {
		if r.err != nil {
			continue
		}
		for _, c := range r.refs[:min(len(r.refs), maxFrames)] {
			cands = append(cands, c)
			candApp = append(candApp, i)
		}
	}
	calls = calls[:0]
	for _, c := range cands {
		calls = append(calls,
			call{op: opRole, name: c.name, path: c.path},
			call{op: opState, name: c.name, path: c.path},
			call{op: opExtents, name: c.name, path: c.path, arg: coordWindow})
	}
	info := do(calls)
	type frame struct {
		frameInfo
		pid    int
		fx, fy int32
	}
	var frames []frame
	for i, c := range cands {
		role, state, ext := info[3*i], info[3*i+1], info[3*i+2]
		if role.err != nil || state.err != nil || ext.err != nil || !isWindowRole(role.u) || !hasState(state.state, stateShowing) {
			continue
		}
		frames = append(frames, frame{
			frameInfo: frameInfo{ref: c, active: hasState(state.state, stateActive), w: int(ext.rect[2]), h: int(ext.rect[3])},
			pid:       kept[candApp[i]].pid, fx: ext.rect[0], fy: ext.rect[1],
		})
	}

	// Pair the windows with the frames, process by process.
	var matched []winMatch
	for pid := range wanted {
		var wins []ports.HintWindow
		for _, w := range windows {
			if w.PID == pid {
				wins = append(wins, w)
			}
		}
		var fs []frameInfo
		var origin []frame
		for _, f := range frames {
			if f.pid == pid {
				fs = append(fs, f.frameInfo)
				origin = append(origin, f)
			}
		}
		for wi, fi := range matchFrames(wins, fs) {
			matched = append(matched, winMatch{win: wins[wi], frame: origin[fi].ref, fx: origin[fi].fx, fy: origin[fi].fy})
		}
	}

	targets := s.elements(do, matched, linksOnly)
	log := logging.For(ctx, "atspi")
	log.Debug().Int("apps", len(kept)).Int("frames", len(frames)).Int("windows", len(matched)).Int("targets", len(targets)).Msg("atspi targets")
	return targets, nil
}

// elements reads the clickable elements of the matched frames.
func (s *Source) elements(do func([]call) []reply, matched []winMatch, linksOnly bool) []ports.HintTarget {
	calls := make([]call, len(matched))
	for i, m := range matched {
		calls[i] = call{op: opMatches, name: m.frame.name, path: m.frame.path, rule: matchRule{linksOnly: linksOnly}}
	}
	found := make([][]ref, len(matched))
	var walk []int // frames without Collection
	for i, r := range do(calls) {
		if r.err != nil {
			var be *busError
			if errors.As(r.err, &be) {
				walk = append(walk, i)
			}
			continue
		}
		found[i] = r.refs
	}
	for _, i := range walk {
		found[i] = s.walk(do, matched[i].frame, linksOnly)
	}

	calls = calls[:0]
	var owner []int
	for i, refs := range found {
		for _, r := range refs {
			calls = append(calls,
				call{op: opRole, name: r.name, path: r.path},
				call{op: opExtents, name: r.name, path: r.path, arg: coordWindow})
			owner = append(owner, i)
		}
	}
	info := do(calls)
	var out []ports.HintTarget
	for k, i := range owner {
		role, ext := info[2*k], info[2*k+1]
		if role.err == nil && isDocument(role.u) {
			continue
		}
		if role.err != nil || ext.err != nil || !isClickable(role.u, linksOnly) {
			continue
		}
		m := matched[i]
		t, ok := toTarget(m.win, m.fx, m.fy, element{role: role.u, x: ext.rect[0], y: ext.rect[1], w: ext.rect[2], h: ext.rect[3]})
		if !ok {
			continue
		}
		t.Window = m.win.ID
		out = append(out, t)
	}
	return out
}

// walk is the fallback for toolkits without Collection: a breadth-first
// walk of at most maxDepth levels and maxWalkNodes nodes, returning the
// clickable nodes.
func (s *Source) walk(do func([]call) []reply, root ref, linksOnly bool) []ref {
	var out []ref
	level := []ref{root}
	seen := 0
	for depth := 0; depth < maxDepth && len(level) > 0 && seen < maxWalkNodes; depth++ {
		calls := make([]call, 0, 3*len(level))
		for _, n := range level {
			calls = append(calls,
				call{op: opChildren, name: n.name, path: n.path},
				call{op: opRole, name: n.name, path: n.path},
				call{op: opState, name: n.name, path: n.path})
		}
		rep := do(calls)
		room := max(0, maxWalkNodes-seen-len(level))
		next := make([]ref, 0, room)
		for i, n := range level {
			ch, role, state := rep[3*i], rep[3*i+1], rep[3*i+2]
			if state.err == nil && !hasState(state.state, stateShowing) {
				continue
			}
			if role.err == nil && (isClickable(role.u, linksOnly) || isDocument(role.u)) && state.err == nil && hasState(state.state, stateVisible) {
				out = append(out, n)
			}
			if ch.err == nil {
				take := min(len(ch.refs), room-len(next))
				next = append(next, ch.refs[:take]...)
			}
		}
		seen += len(level)
		level = next
	}
	return out
}
