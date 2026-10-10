package watchdog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
)

// stuckOwnerLoop blocks like an owner goroutine that never reaches its
// select again; the dump must name it.
func stuckOwnerLoop(release <-chan struct{}) { <-release }

func TestStalledLoopIsDumpedOnce(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	var mu sync.Mutex
	answered := 0
	probes := func() []Probe {
		return []Probe{
			{Name: "stuck", Ping: func() { stuckOwnerLoop(release) }},
			{Name: "fine", Ping: func() { mu.Lock(); answered++; mu.Unlock() }},
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, probes, Options{Dir: dir, Every: 5 * time.Millisecond, Timeout: 30 * time.Millisecond, Log: zerowrap.Default()})
	}()
	deadline := time.Now().Add(2 * time.Second)
	var dumps []string
	for len(dumps) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		dumps, _ = filepath.Glob(filepath.Join(dir, "stall-*-stuck.txt"))
	}
	if len(dumps) != 1 {
		t.Fatalf("dumps %v", dumps)
	}
	// Still stuck: no second dump.
	time.Sleep(100 * time.Millisecond)
	if again, _ := filepath.Glob(filepath.Join(dir, "stall-*.txt")); len(again) != 1 {
		t.Fatalf("dumps after the first %v", again)
	}
	data, err := os.ReadFile(dumps[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "stuckOwnerLoop") {
		t.Fatalf("dump does not name the stuck function:\n%s", data)
	}
	mu.Lock()
	n := answered
	mu.Unlock()
	if n < 2 {
		t.Fatalf("a healthy loop was probed %d times", n)
	}
	close(release)
	cancel()
	<-done
}
