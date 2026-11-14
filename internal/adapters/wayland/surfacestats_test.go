package wayland

import (
	"bytes"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"bnema.dev/purego-libwayland/server"
	"bnema.dev/zerowrap"
)

func TestSurfaceAcquireStallAndStats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out bytes.Buffer
		srv := &Server{log: zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out}).WithField("component", "wayland"), syncWait: &syncWaiter{}, fifoSurfaces: map[*surface]struct{}{}, lastFlip: map[string]time.Time{}}
		surf := &surface{server: srv, x11win: &window{id: 42, mapped: true}, commitsReceived: 2, commitsApplied: 1, releasePointsSignalled: 3, releasePointsDropped: 4, releasePointsDeferred: 5}
		srv.surfaces = map[*server.Resource]*surface{nil: surf}
		u := &update{owner: surf, enqueued: time.Now(), pendingCommit: pendingCommit{sync: &commitSync{wait: &syncWait{explicit: true}, acquire: syncPoint{tl: &timeline{handle: 7, alive: true, uses: 1}, point: 11}}}}
		surf.queue = []*update{u}
		srv.fifoSurfaces[surf] = struct{}{}
		srv.tickFifo(time.Now(), nil)
		if u.blocked != blockAcquire {
			t.Fatalf("block = %v", u.blocked)
		}
		time.Sleep(501 * time.Millisecond)
		synctest.Wait()
		srv.tickFifo(time.Now(), nil)
		srv.tickFifo(time.Now(), nil)
		if n := bytes.Count(out.Bytes(), []byte("surface commit stalled")); n != 1 {
			t.Fatalf("warnings=%d: %s", n, out.String())
		}
		time.Sleep(10*time.Second - 501*time.Millisecond)
		synctest.Wait()
		srv.reportSurfaceStats(time.Now())
		dec := json.NewDecoder(&out)
		var warning, stats map[string]any
		if err := dec.Decode(&warning); err != nil {
			t.Fatal(err)
		}
		if err := dec.Decode(&stats); err != nil {
			t.Fatal(err)
		}
		for k, want := range map[string]any{"message": "surface stats", "component": "wayland", "window": float64(42), "head_block": "acquire", "head_age_ms": float64(10000), "queue_len": float64(1), "commits_received": float64(2), "commits_applied": float64(1), "release_points_signalled": float64(3), "release_points_dropped": float64(4), "release_points_deferred": float64(5), "sub": false, "surface": float64(0)} {
			if stats[k] != want {
				t.Errorf("%s=%v, want %v", k, stats[k], want)
			}
		}
		if warning["acquire_point"] != float64(11) || warning["acquire_timeline"] != float64(7) || warning["release_points_deferred"] != float64(5) || warning["sub"] != false {
			t.Fatalf("warning: %v", warning)
		}
		srv.reportSurfaceStats(time.Now())
		var next map[string]any
		if err := json.NewDecoder(&out).Decode(&next); err != nil {
			t.Fatal(err)
		}
		if next["commits_received"] != float64(0) || next["release_points_dropped"] != float64(0) || next["release_points_deferred"] != float64(0) {
			t.Fatalf("not interval counts: %v", next)
		}
		// Retire the warned head through the production queue teardown, then
		// capture a new commit. Pool reuse is deliberately not assumed.
		u.sync.release = syncPoint{tl: &timeline{alive: true, uses: 1}, point: 13}
		surf.dropQueue()
		surf.queueUpdateAt(time.Now())
		fresh := surf.queue[0]
		fresh.sync = &commitSync{release: syncPoint{tl: &timeline{alive: true, uses: 1}, point: 14}, wait: &syncWait{explicit: true}, acquire: syncPoint{tl: &timeline{handle: 7, alive: true, uses: 1}, point: 12}}
		if !fresh.enqueued.Equal(time.Now()) {
			t.Fatal("new head age not reset")
		}
		out.Reset()
		srv.tickFifo(time.Now(), nil)
		time.Sleep(501 * time.Millisecond)
		synctest.Wait()
		srv.tickFifo(time.Now(), nil)
		srv.tickFifo(time.Now(), nil)
		var rearmed map[string]any
		if err := json.Unmarshal(out.Bytes(), &rearmed); err != nil {
			t.Fatal(err)
		}
		if rearmed["message"] != "surface commit stalled" || rearmed["head_age_ms"] != float64(501) {
			t.Fatalf("fresh head warning: %v", rearmed)
		}
		surf.dropQueue()
		srv.drainDeferredReleases(true)
		srv.reportSurfaceStats(time.Now()) // consume release/drop activity
		out.Reset()
		srv.reportSurfaceStats(time.Now())
		if out.Len() != 0 {
			t.Fatalf("idle surface logged: %s", out.String())
		}
	})
}

func TestSurfaceBarrierWaitDoesNotWarn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out bytes.Buffer
		srv := &Server{log: zerowrap.New(zerowrap.Config{Output: &out}).WithField("component", "wayland")}
		surf := &surface{server: srv, x11win: &window{id: 42, mapped: true}, barrier: true}
		u := &update{owner: surf, enqueued: time.Now(), pendingCommit: pendingCommit{wait: true}}
		surf.queue = []*update{u}
		if u.graphReady(time.Now()) || u.blocked != blockBarrier {
			t.Fatal("expected barrier wait")
		}
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		surf.reportStall(time.Now())
		// Even an intentionally suspended FIFO barrier is not a stall warning.
		time.Sleep(time.Second)
		synctest.Wait()
		surf.reportStall(time.Now())
		if out.Len() != 0 {
			t.Fatalf("barrier warned: %s", out.String())
		}
	})
}

func TestSurfaceReleaseSignalCount(t *testing.T) {
	dev := newMocksyncobjDevice(t)
	dev.EXPECT().signal(uint32(7), uint64(11)).Return(nil).Once()
	srv := &Server{}
	surf := &surface{server: srv}
	srv.releaseSync(syncHold{owner: surf, release: syncPoint{tl: &timeline{dev: dev, handle: 7, alive: true, uses: 1}, point: 11}})
	if surf.releasePointsSignalled != 1 {
		t.Fatal("successful signal not counted")
	}
}

func TestSubsurfaceStatsAndStallAttribution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out bytes.Buffer
		srv := &Server{log: zerowrap.New(zerowrap.Config{Format: "json", Output: &out}).WithField("component", "wayland")}
		root := &surface{server: srv, x11win: &window{id: 42, mapped: true}}
		child := &surface{server: srv, commitsReceived: 1, releasePointsDeferred: 2}
		child.sub.parent = root
		child.queue = []*update{{enqueued: time.Now(), blocked: blockAcquire}}
		srv.surfaces = map[*server.Resource]*surface{nil: child}
		time.Sleep(time.Second)
		synctest.Wait()
		srv.reportSurfaceStats(time.Now())
		dec := json.NewDecoder(&out)
		for _, message := range []string{"surface commit stalled", "surface stats"} {
			var event map[string]any
			if err := dec.Decode(&event); err != nil {
				t.Fatal(err)
			}
			if event["message"] != message || event["window"] != float64(42) || event["sub"] != true || event["release_points_deferred"] != float64(2) {
				t.Fatalf("attribution: %v", event)
			}
		}
		child.queue = nil
		out.Reset()
		child.releasePointsDeferred++
		srv.reportSurfaceStats(time.Now())
		var event map[string]any
		if err := json.Unmarshal(out.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event["release_points_deferred"] != float64(1) {
			t.Fatalf("deferred-only interval: %v", event)
		}
	})
}
