package watchdog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/zerowrap"
)

// stuckOwnerLoop blocks like an owner goroutine that never reaches its
// select again; the dump must name it.
func stuckOwnerLoop(ctx context.Context, release <-chan struct{}) {
	select {
	case <-release:
	case <-ctx.Done():
	}
}

func TestStalledLoopIsDumpedOnce(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		release := make(chan struct{})
		var answered atomic.Int32
		probes := func() []Probe {
			return []Probe{
				{Name: "stuck", Ping: func() { stuckOwnerLoop(ctx, release) }},
				{Name: "fine", Ping: func() { answered.Add(1) }},
			}
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(ctx, probes, Options{Dir: dir, Every: 10 * time.Millisecond, Timeout: 50 * time.Millisecond, Log: zerowrap.Default()})
		}()
		dumps := func() []string {
			m, _ := filepath.Glob(filepath.Join(dir, "stall-*.txt"))
			return m
		}
		time.Sleep(40 * time.Millisecond)
		synctest.Wait()
		if d := dumps(); len(d) != 0 {
			t.Fatalf("dumped before the timeout: %v", d)
		}
		time.Sleep(30 * time.Millisecond)
		synctest.Wait()
		d := dumps()
		if len(d) != 1 || !strings.HasSuffix(d[0], "-stuck.txt") {
			t.Fatalf("dumps %v", d)
		}
		// Still stuck: no second dump.
		time.Sleep(time.Second)
		synctest.Wait()
		if again := dumps(); len(again) != 1 {
			t.Fatalf("dumps after the first %v", again)
		}
		data, err := os.ReadFile(d[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "stuckOwnerLoop") {
			t.Fatalf("dump does not name the stuck function:\n%s", data)
		}
		if n := answered.Load(); n < 50 {
			t.Fatalf("a healthy loop was probed %d times in 1 s", n)
		}
		// Run waits for its probes: a stuck one ends with ctx.
		cancel()
		<-done
	})
}

// With the production cadence (a round every 5 s, a 3 s timeout) a stall is
// dumped at the second round after it began, and a healthy loop is probed
// once per round only.
func TestSparseRoundsDumpAtSecondRound(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		release := make(chan struct{})
		var answered atomic.Int32
		probes := func() []Probe {
			return []Probe{
				{Name: "stuck", Ping: func() { stuckOwnerLoop(ctx, release) }},
				{Name: "fine", Ping: func() { answered.Add(1) }},
			}
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(ctx, probes, Options{Dir: dir, Every: 5 * time.Second, Timeout: 3 * time.Second, Log: zerowrap.Default()})
		}()
		dumps := func() int {
			m, _ := filepath.Glob(filepath.Join(dir, "stall-*.txt"))
			return len(m)
		}
		time.Sleep(9 * time.Second)
		synctest.Wait()
		if n := dumps(); n != 0 {
			t.Fatalf("%d dumps before the second round", n)
		}
		time.Sleep(2 * time.Second) // t = 11 s: second round ran at 10 s
		synctest.Wait()
		if n := dumps(); n != 1 {
			t.Fatalf("%d dumps after the second round, want 1", n)
		}
		if n := answered.Load(); n != 2 {
			t.Fatalf("healthy loop probed %d times in two rounds", n)
		}
		cancel()
		<-done
	})
}
