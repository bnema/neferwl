package wayland

import (
	"bytes"
	"encoding/json"
	"testing"

	"bnema.dev/zerowrap"
)

func TestDropSyncUnreadyWarnsAndCounts(t *testing.T) {
	var out bytes.Buffer
	dev := newMocksyncobjDevice(t)
	dev.EXPECT().destroy(uint32(7)).Return(nil).Once()
	s := &surface{server: &Server{log: zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &out}).WithField("component", "wayland")}}
	acquire := &timeline{dev: dev, handle: 7, uses: 1}
	release := &timeline{dev: dev, handle: 8, alive: true, uses: 1}
	s.dropSync(&commitSync{acquire: syncPoint{tl: acquire, point: 11}, release: syncPoint{tl: release, point: 12}})
	if s.releasePointsDropped != 1 || acquire.uses != 0 || release.uses != 0 {
		t.Fatalf("accounting: dropped=%d acquire=%d release=%d", s.releasePointsDropped, acquire.uses, release.uses)
	}
	var event map[string]any
	if err := json.Unmarshal(out.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"component": "wayland", "level": "warn", "message": "surface release point dropped without signal",
		"window": float64(0), "acquire_point": float64(11), "acquire_timeline": float64(7),
		"release_point": float64(12), "release_timeline": float64(8), "release_points_dropped": float64(1),
	} {
		if event[key] != want {
			t.Errorf("%s = %v, want %v", key, event[key], want)
		}
	}
	s.dropSync(nil)
	if s.releasePointsDropped != 1 {
		t.Fatal("nil sync counted as a drop")
	}
}
