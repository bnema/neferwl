// Package watchdog writes every goroutine stack to a file when a probed
// owner loop stops answering, so a frozen session leaves the exact place
// it is stuck on disk.
package watchdog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"github.com/bnema/zerowrap"
)

// Probe asks an owner loop to run an empty task: Ping returns once the
// loop ran it. A loop that is stuck never returns.
type Probe struct {
	Name string
	Ping func()
}

// Options set how often the probes run and how long a loop may take.
type Options struct {
	Dir     string        // where stall dumps go (the run log directory)
	Every   time.Duration // between two probes of a loop
	Timeout time.Duration // a probe slower than this is a stall
	Log     zerowrap.Logger
}

// Run probes each loop probes returns until ctx ends; the set may change
// between ticks (outputs come and go). On a stall it writes one dump per
// stall: a loop must answer again before it is dumped again.
func Run(ctx context.Context, probes func() []Probe, o Options) {
	type state struct {
		answer  chan struct{} // the probe in flight, nil when none
		since   time.Time
		stalled bool
		seen    bool
	}
	states := map[string]*state{}
	tick := time.NewTicker(o.Every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			for _, s := range states {
				s.seen = false
			}
			for _, p := range probes() {
				s := states[p.Name]
				if s == nil {
					s = &state{}
					states[p.Name] = s
				}
				s.seen = true
				if s.answer != nil {
					select {
					case <-s.answer:
						if s.stalled {
							o.Log.Warn().Str("loop", p.Name).Dur("after", now.Sub(s.since)).Msg("loop answers again")
						}
						s.answer, s.stalled = nil, false
					default:
						if !s.stalled && now.Sub(s.since) >= o.Timeout {
							s.stalled = true
							path, err := dump(o.Dir, p.Name, now)
							o.Log.Error().Err(err).Str("loop", p.Name).Dur("waited", now.Sub(s.since)).Str("dump", path).Msg("loop stalled")
						}
						continue // one probe in flight per loop
					}
				}
				answer := make(chan struct{})
				s.answer, s.since = answer, now
				go func() { p.Ping(); close(answer) }()
			}
			// A loop that left (an unplugged output) is forgotten; its
			// probe goroutine ends with the loop's context.
			for name, s := range states {
				if !s.seen {
					delete(states, name)
				}
			}
		}
	}
}

// dump writes every goroutine stack to dir and syncs it: a frozen machine
// is often force rebooted, which loses what the page cache still holds.
func dump(dir, name string, now time.Time) (string, error) {
	path := filepath.Join(dir, fmt.Sprintf("stall-%s-%s.txt", now.Format("20060102-150405.000"), name))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return path, err
	}
	fmt.Fprintf(f, "loop %q stopped answering at %s\n\n", name, now.Format(time.RFC3339Nano))
	werr := pprof.Lookup("goroutine").WriteTo(f, 2)
	serr := f.Sync()
	cerr := f.Close()
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	for _, err := range []error{werr, serr, cerr} {
		if err != nil {
			return path, err
		}
	}
	return path, nil
}
