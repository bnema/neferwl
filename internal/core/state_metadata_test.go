package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Content hints survive mapping and update the published state without a layout change.
func TestStateContentType(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, nil, left)
		r.client <- ports.WindowMapped{ID: 1, ContentType: domain.ContentPhoto}
		receive(t, r.scenes)
		waitState(t, r.state, "map photo", func(w ports.WindowState) bool { return w.ID == 1 && w.ContentType == "photo" })
		for _, tc := range []struct {
			kind domain.ContentType
			want string
		}{
			{domain.ContentVideo, "video"}, {domain.ContentGame, "game"}, {domain.ContentNone, ""}, {domain.ContentPhoto, "photo"},
		} {
			r.client <- ports.WindowContentType{ID: 1, Type: tc.kind}
			waitState(t, r.state, "changed hint", func(w ports.WindowState) bool { return w.ID == 1 && w.ContentType == tc.want })
		}
		r.client <- ports.WindowContentType{ID: 1, Type: domain.ContentPhoto}
		synctest.Wait()
		select {
		case st := <-r.state:
			t.Fatalf("unchanged hint published: %+v", st)
		default:
		}
		r.client <- ports.WindowContentType{ID: 999, Type: domain.ContentGame}
		synctest.Wait()
		select {
		case st := <-r.state:
			t.Fatalf("unknown window published: %+v", st)
		default:
		}
	})
}
