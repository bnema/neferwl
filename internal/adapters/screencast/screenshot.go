package screencast

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Screenshots are taken by nefercap, run as a child process:
//
//	Screenshot            nefercap screenshot -output FOCUSED -file F
//	ScreenshotArea        nefercap screenshot -output O -region X,Y,WxH -file F
//	InteractiveScreenshot nefercap shot -file F
//
// F is $XDG_RUNTIME_DIR/neferwl/screenshot-<unixnano>.png. One run at most
// is in flight. The run is a goroutine; its result goes back through the
// service's transport and the owner sends the reply, so the bus is still
// used by the owner only. SelectArea and ScreenshotWindow (the "Select
// area" and "Window" choices of the GNOME dialog) are not supported;
// include_cursor and flash are ignored.

// shotState is the screenshot in flight; one at most.
type shotState struct {
	busy bool
	req  request
	uri  bool // reply with a file:// URI
	path string
}

// shotResult is what a finished nefercap run leaves for the owner.
type shotResult struct {
	ready bool
	err   error
}

// screenshot answers Screenshot, ScreenshotArea and InteractiveScreenshot.
func (s *Service) screenshot(req *request) {
	if s.shot.busy {
		s.replyErr(req, errLimits, "a screenshot is already being taken")
		return
	}
	var args []string
	dir := shotDir()
	if dir == "" {
		s.replyErr(req, errFailed, "no runtime directory")
		return
	}
	path := filepath.Join(dir, "screenshot-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".png")
	switch req.method {
	case mShot:
		out := s.inv.Focused
		if out == "" && len(s.inv.Outputs) > 0 {
			out = s.inv.Outputs[0].Info.Name
		}
		if out == "" {
			s.replyErr(req, errFailed, "no output")
			return
		}
		args = []string{"screenshot", "-output", out, "-file", path}
	case mShotArea:
		out, region, ok := s.areaTarget(req.x, req.y, req.w, req.h)
		if !ok {
			s.replyErr(req, errInvalidArgs, "area outside every output")
			return
		}
		args = []string{"screenshot", "-output", out, "-region", region, "-file", path}
	default:
		args = []string{"shot", "-file", path}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.log.Warn().Err(err).Msg("screenshot directory not created")
		s.replyErr(req, errFailed, "screenshot directory not created")
		return
	}
	s.shot = shotState{busy: true, req: *req, uri: req.method == mShotInteractive, path: path}
	s.runShot(s.ctx, args)
}

// shotDir is $XDG_RUNTIME_DIR/neferwl.
func shotDir() string {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		return ""
	}
	return filepath.Join(rt, "neferwl")
}

// areaTarget finds the output that contains the corner (x, y) of an area in
// the logical space and the output-local region, clipped to the output.
func (s *Service) areaTarget(x, y, w, h int32) (string, string, bool) {
	if w <= 0 || h <= 0 {
		return "", "", false
	}
	for i := range s.inv.Outputs {
		o := &s.inv.Outputs[i]
		lx, ly := int(x)-o.X, int(y)-o.Y
		if lx < 0 || ly < 0 || lx >= o.Width || ly >= o.Height {
			continue
		}
		cw, ch := min(int(w), o.Width-lx), min(int(h), o.Height-ly)
		return o.Info.Name, strconv.Itoa(lx) + "," + strconv.Itoa(ly) + "," + strconv.Itoa(cw) + "x" + strconv.Itoa(ch), true
	}
	return "", "", false
}

// runShot runs nefercap in a goroutine and leaves the result for the owner.
func (s *Service) runShot(parent context.Context, args []string) {
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		ctx, cancel := context.WithTimeout(parent, shotTimeout)
		defer cancel()
		err := s.runner.Run(ctx, args)
		s.mu.Lock()
		s.shotRes = shotResult{ready: true, err: err}
		s.woken = true
		s.mu.Unlock()
		s.wake()
	}()
}

// onShotDone sends the reply of the screenshot that finished.
func (s *Service) onShotDone(r *shotResult) {
	st := s.shot
	s.shot = shotState{}
	if !st.busy {
		return
	}
	if r.err != nil {
		s.log.Warn().Err(r.err).Msg("screenshot failed")
		_ = os.Remove(st.path)
		s.replyErr(&st.req, errFailed, "screenshot failed")
		return
	}
	out := st.path
	if st.uri {
		out = "file://" + out
	}
	e := s.replyTo(&st.req, "bs")
	if e == nil {
		return
	}
	e.Bool(true)
	e.Str(out)
	s.send()
}
