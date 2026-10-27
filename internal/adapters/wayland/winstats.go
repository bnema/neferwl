package wayland

import (
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// frameRing keeps the last ports.StatsFrames frame times in microseconds.
type frameRing struct {
	buf  [ports.StatsFrames]uint32
	n    int // samples held, ≤ len(buf)
	head int // next write index
}

// frameSummary is the statistics of a frameRing; FPS figures are client FPS.
type frameSummary struct {
	Avg, Min, Max, Low1, Low01 float32
	Graph                      [ports.StatsGraph]uint16
}

// push records a frame time; 0 µs is ignored.
func (r *frameRing) push(us uint32) {
	if us == 0 {
		return
	}
	r.buf[r.head] = us
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// fps is 1e6 / mean frame time over the held samples; 0 when n == 0.
func (r *frameRing) fps() float32 {
	if r.n == 0 {
		return 0
	}
	var sum uint64
	for _, v := range r.buf[:r.n] {
		sum += uint64(v)
	}
	return meanFPS(sum, r.n)
}

// meanFPS is the FPS of count frames lasting sum µs in total.
func meanFPS(sum uint64, count int) float32 {
	if count == 0 || sum == 0 {
		return 0
	}
	return float32(1e6 * float64(count) / float64(sum))
}

// summary fills avg/min/max/low1/low01 FPS and the last ports.StatsGraph
// frame times (oldest first, capped at 65535 µs, zero-padded at the front
// when fewer samples). scratch is reused for sorting; it must hold
// ports.StatsFrames values.
func (r *frameRing) summary(scratch *[ports.StatsFrames]uint32) frameSummary {
	var s frameSummary
	if r.n == 0 {
		return s
	}
	n := r.n
	// The oldest sample sits at head when full, at 0 otherwise.
	start := (r.head - n + len(r.buf)) % len(r.buf)
	for i := range n {
		scratch[i] = r.buf[(start+i)%len(r.buf)]
	}
	for i := range ports.StatsGraph {
		k := n - ports.StatsGraph + i
		if k < 0 {
			continue
		}
		s.Graph[i] = uint16(min(scratch[k], 65535))
	}
	sorted := scratch[:n]
	slices.Sort(sorted)
	var sum uint64
	for _, v := range sorted {
		sum += uint64(v)
	}
	s.Avg = meanFPS(sum, n)
	s.Min = meanFPS(uint64(sorted[n-1]), 1)
	s.Max = meanFPS(uint64(sorted[0]), 1)
	s.Low1 = lowFPS(sorted, (n+99)/100)
	s.Low01 = lowFPS(sorted, (n+999)/1000)
	return s
}

// lowFPS is the FPS of the k slowest frames of ascending sorted.
func lowFPS(sorted []uint32, k int) float32 {
	k = max(k, 1)
	var sum uint64
	for _, v := range sorted[len(sorted)-k:] {
		sum += uint64(v)
	}
	return meanFPS(sum, k)
}

// windowStats belongs to the display goroutine and tracks one window.
type windowStats struct {
	watched       domain.WindowID
	gen           uint64
	timer         ports.Timer
	client, shown frameRing
	lastCommit    time.Time
	lastShown     time.Duration
	lastShownSeq  uint64
	facts         ports.WindowStats
	scratch       [ports.StatsFrames]uint32
}

// commit records a fresh root buffer and its presentation hints.
func (w *windowStats) commit(now time.Time, c domain.SurfaceContent, explicitSync, tearing bool) {
	if w.watched == 0 {
		return
	}
	if !w.lastCommit.IsZero() && now.After(w.lastCommit) {
		w.client.push(uint32(min(now.Sub(w.lastCommit).Microseconds(), int64(^uint32(0)))))
	}
	w.lastCommit = now
	w.bufferFacts(c, explicitSync, tearing)
}

// bufferFacts refreshes committed buffer facts without recording a frame time.
func (w *windowStats) bufferFacts(c domain.SurfaceContent, explicitSync, tearing bool) {
	w.facts.Width, w.facts.Height = c.Width, c.Height
	w.facts.DMABuf = c.DMABuf != nil
	w.facts.Format, w.facts.Modifier = 0, 0
	if c.DMABuf != nil {
		w.facts.Format, w.facts.Modifier = c.DMABuf.Format, c.DMABuf.Modifier
	}
	w.facts.ExplicitSync, w.facts.Tearing = explicitSync, tearing
}

// flip records only advancing content sequences, but refreshes output facts on every showing.
func (w *windowStats) flip(f *ports.FlipInfo) {
	if w.watched == 0 {
		return
	}
	seq, ok := f.Shows[w.watched]
	if !ok {
		return
	}
	if seq > w.lastShownSeq {
		if w.lastShownSeq != 0 && f.When > w.lastShown {
			w.shown.push(uint32(min((f.When - w.lastShown).Microseconds(), int64(^uint32(0)))))
		}
		w.lastShown, w.lastShownSeq = f.When, seq
	}
	w.facts.Scanout = f.ZeroCopy == w.watched
	w.facts.VRR = f.Refresh == 0 && f.HardwareClock
	w.facts.Async = f.Async
	w.facts.ScanoutReason, w.facts.OverlayReason = f.ScanoutReason, f.OverlayReason
}

// watchStats replaces the watched window and invalidates pending timer callbacks.
func (s *Server) watchStats(id domain.WindowID) {
	w := s.windows[id]
	if w == nil || !w.mapped {
		id = 0
	}
	if id == s.stats.watched {
		return
	}
	if s.stats.timer != nil {
		s.stats.timer.Stop()
	}
	gen := s.stats.gen + 1
	s.stats = windowStats{watched: id, gen: gen}
	if id != 0 {
		root := w.xdg.surface
		s.stats.bufferFacts(root.content, root.hold.acquire != nil, root.async)
		s.armStats()
	}
}

// armStats schedules one report, returning ownership to the display goroutine.
func (s *Server) armStats() {
	gen := s.stats.gen
	s.stats.timer = s.clock.AfterFunc(ports.StatsInterval, func() {
		s.display.Do(func() {
			if s.stats.gen != gen || s.stats.watched == 0 {
				return
			}
			s.emit(s.stats.snapshot())
			s.armStats()
		})
	})
}

// snapshot copies the last facts and summarizes the retained client timings.
func (w *windowStats) snapshot() ports.WindowStats {
	result := w.facts
	result.Window = w.watched
	result.ClientFPS, result.ShownFPS = w.client.fps(), w.shown.fps()
	summary := w.client.summary(&w.scratch)
	result.Avg, result.Min, result.Max = summary.Avg, summary.Min, summary.Max
	result.Low1, result.Low01, result.Frametimes = summary.Low1, summary.Low01, summary.Graph
	return result
}
