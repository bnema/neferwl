package wayland

import (
	"time"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/presentationtime"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// wp_presentation: a client learns when each commit reached the screen.
// A feedback belongs to the commit it was requested before; on commit it
// waits for the flip that shows its window's content (FlipInfo.Shows)
// and gets that flip's kernel timestamp, vblank counter and refresh.
// A content replaced before any flip showed it, a destroyed surface or a
// surface on no output is discarded.

const presentationVersion = 2

// feedbackWait is a committed feedback: presented once a flip on the
// surface's output shows content seq of window win.
type feedbackWait struct {
	fb   *presentationtime.WpPresentationFeedback
	surf *surface
	win  ports.WindowID
	seq  uint64
	at   time.Time
	// replaced is the window content Seq of the surface's next commit (0:
	// none yet): a flip showing it or later shows the replacement.
	replaced uint64
}

func registerPresentation(d *server.Display, s *Server) error {
	return presentationtime.NewWpPresentationGlobal(d, presentationVersion, func(c server.Client, v, id uint32) {
		r, err := presentationtime.NewWpPresentation(c, int32(v), id, presentation{s})
		if err == nil {
			r.SendClockId(unix.CLOCK_MONOTONIC)
		}
	})
}

type presentation struct{ server *Server }

func (presentation) Destroy(*presentationtime.WpPresentation) {}

// Feedback attaches a feedback to the surface's next commit.
func (p presentation) Feedback(r *presentationtime.WpPresentation, surf *wayland.Surface, id uint32) {
	fb, err := presentationtime.NewWpPresentationFeedback(r.Client(), r.Version(), id, feedbackHandler{})
	if err != nil {
		return
	}
	state := p.server.surfaceOf(surf)
	if state == nil || state.destroyed {
		fb.SendDiscarded()
		fb.Destroy()
		return
	}
	state.pendingFeedback = append(state.pendingFeedback, fb)
}

type feedbackHandler struct{}

// Destroy is not a request of wp_presentation_feedback; the handler
// satisfies the generated interface.
func (feedbackHandler) Destroy(*presentationtime.WpPresentationFeedback) {}

// commitFeedback moves a surface's pending feedbacks to its window's
// content just emitted, or discards them when the surface shows nowhere.
// A commit with a new buffer (fresh) replaces the surface's earlier
// waiting feedbacks.
func (s *surface) commitFeedback(pending []*presentationtime.WpPresentationFeedback, fresh bool) {
	srv := s.server
	root := s.root()
	win := root.windowID()
	if win != 0 && fresh {
		srv.contentMu.Lock()
		seq := srv.contentSeq[win]
		srv.contentMu.Unlock()
		for i := range srv.feedbacks {
			if w := &srv.feedbacks[i]; w.surf == s && w.replaced == 0 && seq > w.seq {
				w.replaced = seq
			}
		}
	}
	if len(pending) == 0 {
		return
	}
	output := srv.frameOutput(s)
	if win == 0 || output == "" {
		for _, fb := range pending {
			discard(fb)
		}
		return
	}
	srv.contentMu.Lock()
	seq := srv.contentSeq[win]
	srv.contentMu.Unlock()
	for _, fb := range pending {
		srv.feedbacks = append(srv.feedbacks, feedbackWait{fb: fb, surf: s, win: win, seq: seq, at: time.Now()})
	}
}

func discard(fb *presentationtime.WpPresentationFeedback) {
	if fb.Resource.Alive() {
		fb.SendDiscarded()
		fb.Destroy()
	}
}

// presentFlip answers the feedbacks a flip of output answers: a window
// content at or after a feedback's is shown; the feedback is presented,
// or discarded when its surface committed again by then.
func (s *Server) presentFlip(output string, f *ports.FlipInfo) {
	if f == nil || len(s.feedbacks) == 0 {
		return
	}
	o := s.outputByNameExact(output)
	kept := s.feedbacks[:0]
	for _, w := range s.feedbacks {
		shown, ok := f.Shows[w.win]
		switch {
		case !w.fb.Resource.Alive():
		case w.surf.destroyed:
			discard(w.fb)
		case s.frameOutput(w.surf) != output || !ok || shown < w.seq:
			kept = append(kept, w)
		case f.Merged > 0:
			// The reader fell behind: which flip showed it is unknown.
			discard(w.fb)
		default:
			// Shown, unless the surface's next commit is shown too.
			s.sendPresented(w, o, f, w.replaced == 0 || shown < w.replaced)
		}
	}
	clear(s.feedbacks[len(kept):])
	s.feedbacks = kept
}

// sendPresented sends presented for the flip; exact is false when a
// later commit of the surface replaced it before the flip (then it is
// discarded).
func (s *Server) sendPresented(w feedbackWait, o *output, f *ports.FlipInfo, exact bool) {
	if !exact {
		discard(w.fb)
		return
	}
	if o != nil {
		for _, r := range o.resources {
			if r.Resource.Alive() && r.Client() == w.fb.Client() {
				w.fb.SendSyncOutput(r)
			}
		}
	}
	flags := uint32(0)
	// A software clock (headless) claims nothing about the display.
	if !f.Async && f.HardwareClock {
		flags |= uint32(presentationtime.WpPresentationFeedbackKindVsync)
	}
	if f.HardwareClock {
		flags |= uint32(presentationtime.WpPresentationFeedbackKindHwClock | presentationtime.WpPresentationFeedbackKindHwCompletion)
	}
	if f.ZeroCopy {
		flags |= uint32(presentationtime.WpPresentationFeedbackKindZeroCopy)
	}
	sec, nsec := uint64(f.When/time.Second), uint32(f.When%time.Second)
	w.fb.SendPresented(uint32(sec>>32), uint32(sec), nsec, uint32(f.Refresh), uint32(f.Seq>>32), uint32(f.Seq), flags)
	w.fb.Destroy()
}

// feedbackTimeout discards a feedback no flip answered: its window is
// hidden, on an output that does not flip (headless, switched away), or
// was never drawn.
const feedbackTimeout = time.Second

// dropFeedbacks discards the feedbacks of destroyed surfaces, of outputs
// that are gone, and those older than feedbackTimeout.
func (s *Server) dropFeedbacks(now time.Time) {
	kept := s.feedbacks[:0]
	for _, w := range s.feedbacks {
		if !w.fb.Resource.Alive() {
			continue
		}
		if w.surf.destroyed || s.frameOutput(w.surf) == "" || now.Sub(w.at) > feedbackTimeout {
			discard(w.fb)
			continue
		}
		kept = append(kept, w)
	}
	clear(s.feedbacks[len(kept):])
	s.feedbacks = kept
}
