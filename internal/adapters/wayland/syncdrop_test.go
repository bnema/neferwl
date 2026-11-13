package wayland

import (
	"bytes"
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
	srv := &Server{log: zerowrap.New(zerowrap.Config{Output: &out}).WithField("component", "wayland")}
	surf := &surface{server: srv}
	tl := &timeline{alive: true, uses: 2}
	surf.dropSync(&commitSync{acquire: syncPoint{tl: tl, point: 11}, release: syncPoint{tl: tl, point: 12}})
	surf.dropSync(nil)
	if tl.uses != 0 || surf.releasePointsDropped != 1 || !bytes.Contains(out.Bytes(), []byte("surface release point dropped without signal")) {
		t.Fatal("missing wait must be abandoned and warned")
	}
}
