package wayland

import (
	"bytes"
	"encoding/json"
	"testing"

	"bnema.dev/zerowrap"
)

func TestDropSyncDeferredLifecycle(t *testing.T) {
	for _, outcome := range []string{"ready", "failed", "acquire_destroyed", "release_destroyed", "shutdown", "already_ready"} {
		t.Run(outcome, func(t *testing.T) {
			var out bytes.Buffer
			dev := newMocksyncobjDevice(t)
			srv := &Server{syncWait: &syncWaiter{}, log: zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &out}).WithField("component", "wayland")}
			surf := &surface{server: srv}
			surf.sub.parent = &surface{server: srv, x11win: &window{id: 42, mapped: true}}
			acquire := &timeline{dev: dev, handle: 7, alive: true, uses: 1}
			release := &timeline{dev: dev, handle: 8, alive: true, uses: 1}
			w := &syncWait{explicit: true, efd: -1, done: outcome == "already_ready"}
			cs := commitSync{acquire: syncPoint{tl: acquire, point: 11}, release: syncPoint{tl: release, point: 12}, wait: w}
			signals := outcome == "ready" || outcome == "already_ready"
			if signals {
				dev.EXPECT().signal(uint32(8), uint64(12)).Return(nil).Once()
			}
			surf.dropSync(&cs)
			if outcome != "already_ready" {
				if surf.releasePointsDeferred != 1 {
					t.Fatal("deferred counter not incremented")
				}
				var event map[string]any
				if err := json.NewDecoder(bytes.NewReader(out.Bytes())).Decode(&event); err != nil {
					t.Fatal(err)
				}
				if event["level"] != "debug" || event["window"] != float64(42) || event["sub"] != true {
					t.Fatalf("deferred attribution: %v", event)
				}
				if len(srv.deferredReleases) != 1 || w.done || acquire.uses != 1 || release.uses != 1 || surf.releasePointsDropped != 0 {
					t.Fatalf("ownership not retained: deferred=%d done=%v uses=%d/%d dropped=%d", len(srv.deferredReleases), w.done, acquire.uses, release.uses, surf.releasePointsDropped)
				}
				// Retiring/reusing the original update must not change deferred points.
				cs = commitSync{}
				srv.drainDeferredReleases(false)
				if len(srv.deferredReleases) != 1 {
					t.Fatal("unready acquire was drained")
				}
				switch outcome {
				case "ready":
					w.done = true
				case "failed":
					w.done, w.failed = true, true
				case "acquire_destroyed":
					acquire.alive = false
					dev.EXPECT().destroy(uint32(7)).Return(nil).Once()
				case "release_destroyed":
					release.alive = false
					dev.EXPECT().destroy(uint32(8)).Return(nil).Once()
				}
				srv.drainDeferredReleases(outcome == "shutdown")
			}
			srv.drainDeferredReleases(false)
			if len(srv.deferredReleases) != 0 || acquire.uses != 0 || release.uses != 0 || !w.done {
				t.Fatalf("ownership leaked: deferred=%d uses=%d/%d done=%v", len(srv.deferredReleases), acquire.uses, release.uses, w.done)
			}
			if signals {
				if surf.releasePointsSignalled != 1 || surf.releasePointsDropped != 0 {
					t.Fatal("signal accounting")
				}
				if bytes.Contains(out.Bytes(), []byte("surface release point dropped without signal")) {
					t.Fatalf("successful release warned: %s", out.String())
				}
			} else {
				if surf.releasePointsSignalled != 0 || surf.releasePointsDropped != 1 {
					t.Fatal("abandon accounting")
				}
				if n := bytes.Count(out.Bytes(), []byte("surface release point dropped without signal")); n != 1 {
					t.Fatalf("warnings=%d: %s", n, out.String())
				}
			}
			if !signals {
				want := "timeline_dead"
				if outcome == "failed" {
					want = "wait_failed"
				}
				if outcome == "shutdown" {
					want = "shutdown"
				}
				dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
				var deferred, warning map[string]any
				if err := dec.Decode(&deferred); err != nil {
					t.Fatal(err)
				}
				if err := dec.Decode(&warning); err != nil {
					t.Fatal(err)
				}
				if warning["reason"] != want || warning["window"] != float64(42) || warning["sub"] != true {
					t.Fatalf("abandon diagnostic: %v", warning)
				}
			}
			if outcome == "already_ready" && surf.releasePointsDeferred != 0 {
				t.Fatal("ready release counted as deferred")
			}
			// Destroy remaining live timelines after the references have retired.
			if acquire.alive {
				dev.EXPECT().destroy(uint32(7)).Return(nil).Once()
				acquire.alive = false
				srv.dropTimeline(acquire)
			}
			if release.alive {
				dev.EXPECT().destroy(uint32(8)).Return(nil).Once()
				release.alive = false
				srv.dropTimeline(release)
			}
			if acquire.handle != 0 || release.handle != 0 {
				t.Fatal("timeline handles leaked")
			}
		})
	}
}

func TestDropSyncMissingWaitWarns(t *testing.T) {
	var out bytes.Buffer
	srv := &Server{log: zerowrap.New(zerowrap.Config{Format: "json", Output: &out}).WithField("component", "wayland")}
	surf := &surface{server: srv}
	tl := &timeline{alive: true, uses: 2}
	surf.dropSync(&commitSync{acquire: syncPoint{tl: tl, point: 11}, release: syncPoint{tl: tl, point: 12}})
	surf.dropSync(nil)
	var event map[string]any
	if err := json.Unmarshal(out.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["reason"] != "no_wait" || event["sub"] != false {
		t.Fatalf("missing wait diagnostic: %v", event)
	}
	if tl.uses != 0 || surf.releasePointsDropped != 1 || !bytes.Contains(out.Bytes(), []byte("surface release point dropped without signal")) {
		t.Fatal("missing wait must be abandoned and warned")
	}
}
