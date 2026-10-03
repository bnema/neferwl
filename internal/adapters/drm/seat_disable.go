package drm

import "time"

// A seat disable revokes this process as DRM master. The planes keep what
// was last committed, and a client without DRM_CLIENT_CAP_PLANE_COLOR_PIPELINE
// cannot clear COLOR_PIPELINE (amdgpu applies it regardless): the next master
// would inherit the pipeline. So before the disable is acked, every output
// that applies one commits Bypass. (A crash cannot do that: see docs/config.md.)

// PrepareSeatDisable asks the output's goroutine to leave its planes on
// Bypass and waits for it, at most timeout. It reports false on timeout (the
// output is busy or not running): the caller goes on. It is safe to call
// from any goroutine.
func (o *Output) PrepareSeatDisable(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ack := make(chan struct{}, 1) // buffered: Run never waits for us
	select {
	case o.seatDisable <- ack:
	case <-timer.C:
		return false
	}
	select {
	case <-ack:
		return true
	case <-timer.C:
		return false
	}
}

// pipelineApplied reports whether a plane applies the colour pipeline.
func (o *Output) pipelineApplied() bool {
	for _, p := range [...]*plane{o.primary, o.overlay} {
		if p.hasColor() && p.colorKnown && p.colorApplied != colorBypass {
			return true
		}
	}
	return false
}

// bypassForSeatDisable commits Bypass on the planes when one applies the
// pipeline, synchronously. It is a no-op (no commit) otherwise. A failure is
// only logged: the seat is disabled anyway.
func (o *Output) bypassForSeatDisable() {
	if !o.pipelineApplied() {
		return
	}
	req := &o.stateReq
	req.reset()
	o.forceBypass(req)
	if err := o.k.commit(req, 0, 0); err != nil {
		o.log.Warn().Str("component", "drm").Err(err).Str("connector", o.conn.name).Msg("colour pipeline not reset before seat disable")
		return
	}
	o.colorBypassed()
	o.log.Info().Str("component", "drm").Str("connector", o.conn.name).Msg("colour pipeline reset before seat disable")
}
