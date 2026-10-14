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
// is in flight. The run is a goroutine; its result, tagged with the ID of
// the job, goes back through the service's transport and the owner sends
// the reply, so the bus is still used by the owner only. A result whose ID
// is not the job in flight is dropped. A job belongs to one bus attempt:
// when the attempt ends, the job is cancelled and joined before the state is
// reset (stopShot). A run that exits cleanly is a success only if it left a
// non-empty regular file at F; otherwise the reply is (false, ""), which is
// also how a cancelled interactive screenshot is answered. SelectArea and ScreenshotWindow (the "Select
// area" and "Window" choices of the GNOME dialog) are not supported;
// include_cursor and flash are ignored.

// shotState is the screenshot in flight; one at most.
type shotState struct {
	busy   bool
	id     uint64 // names the job; results of other jobs are stale
	req    request
	uri    bool // reply with a file:// URI
	path   string
	cancel context.CancelFunc // stops the run
	done   chan struct{}      // closed when the job goroutine is past its result
}

// shotResult is what a finished nefercap run leaves for the owner.
type shotResult struct {
	ready bool
	id    uint64 // the job it belongs to
	err   error  // nefercap failed
	made  bool   // the output file exists: a regular, non-empty file
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
	s.shotSeq++
	s.shot = shotState{busy: true, id: s.shotSeq, req: *req, uri: req.method == mShotInteractive, path: path}
	s.runShot(args)
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

// runShot runs nefercap in a goroutine for the job in s.shot and leaves the
// tagged result for the owner. The run ends with the attempt's context.
func (s *Service) runShot(args []string) {
	ctx, cancel := context.WithTimeout(s.ctx, shotTimeout)
	done := make(chan struct{})
	s.shot.cancel, s.shot.done = cancel, done
	id, path := s.shot.id, s.shot.path
	run := s.runner
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer close(done)
		defer cancel()
		err := run.Run(ctx, args)
		res := shotResult{ready: true, id: id, err: err}
		if err == nil {
			res.made = isOutputFile(path)
		}
		s.mu.Lock()
		s.shotRes = res
		s.woken = true
		s.mu.Unlock()
		s.wake()
	}()
}

// isOutputFile reports whether path is a regular, non-empty file. A symlink
// is not followed: it is not a regular file.
func isOutputFile(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// stopShot cancels the screenshot in flight, waits for its goroutine and
// forgets its result, so no job outlives the attempt that started it. The
// requester is told it failed when the bus still carries the answer.
func (s *Service) stopShot() {
	st := s.shot
	if !st.busy {
		return
	}
	st.cancel()
	<-st.done
	s.mu.Lock()
	s.shotRes = shotResult{}
	s.mu.Unlock()
	s.shot = shotState{}
	_ = os.Remove(st.path)
	s.replyErr(&st.req, errFailed, "screenshot cancelled")
}

// onShotDone sends the reply of the screenshot that finished. A result of
// another job than the one in flight is stale and completes nothing.
func (s *Service) onShotDone(r *shotResult) {
	st := s.shot
	if !st.busy || r.id != st.id {
		return
	}
	s.shot = shotState{}
	switch {
	case r.err != nil:
		s.log.Warn().Err(r.err).Msg("screenshot failed")
		_ = os.Remove(st.path)
		s.replyErr(&st.req, errFailed, "screenshot failed")
	case !r.made:
		// nefercap exited cleanly and left nothing usable (an interactive
		// screenshot that was cancelled, for one).
		s.log.Info().Msg("screenshot produced no file")
		_ = os.Remove(st.path)
		if e := s.replyTo(&st.req, "bs"); e != nil {
			e.Bool(false)
			e.Str("")
			s.send()
		}
	default:
		out := st.path
		if st.uri {
			out = "file://" + out
		}
		if e := s.replyTo(&st.req, "bs"); e != nil {
			e.Bool(true)
			e.Str(out)
			s.send()
		}
	}
}
