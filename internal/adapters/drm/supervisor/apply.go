package supervisor

import (
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func (run *outputsRun) complete(d core.ApplyDecision) {
	if !d.Reply {
		return
	}
	run.timer.Stop()
	run.deadline = nil
	logOutputApplied(run.apply.Finished(d.Err).Applied, run.appLog)
}

func (run *outputsRun) configure(cfg domain.Config) {
	run.currentConfig = cfg
	run.op++
	run.timer.Stop()
	run.deadline = nil
	required := map[string]bool{}
	// Desired enabled heads, not the inventory's current mode, require a first modeset.
	w := run.want(run.currentConfig)
	for _, h := range run.currentHeads.Heads {
		if !w.Disabled[h.Info.Name] {
			required[h.Info.Name] = true
		}
	}
	for name := range run.set.Outs {
		if !w.Disabled[name] {
			required[name] = true
		}
	}
	run.progress.Start(run.op, required, run.stopping)
	run.scan()
	running := map[string]domain.OutputInstance{}
	for name := range run.set.Outs {
		running[name] = run.readySources[name]
	}
	d := run.progress.Scanned(running)
	if run.progress.Active {
		run.timer.Reset(applyTimeout)
		run.deadline = run.timer.C()
	}
	run.complete(d)
}

func (run *outputsRun) send(ev ports.OutputEvent) {
	// Only the select is timed: the router logs the longest wait, which
	// tells whether core holds up the backend loop.
	start := time.Now()
	select {
	case run.ch.Events <- ev:
	case <-run.ctx.Done():
	}
	run.set.Router.NoteEventSend(time.Since(start))
}
