package virtuals

import (
	"context"
	"errors"
	"github.com/bnema/neferwl/internal/core"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestHostLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan ports.OutputEvent, 16)
		security := make(chan ports.SecurityBackendEvent, 32)
		ch := routing.Channels{Events: events, SecurityEvents: security}
		set := routing.NewSet(ctx, nil, ch.Routes())
		set.WireSecurity(ch)
		defer func() {
			if err := set.Wait(); err != nil {
				t.Error(err)
			}
		}()
		first := portsmocks.NewMockRenderer(t)
		first.EXPECT().Close().Return().Once()
		second := portsmocks.NewMockRenderer(t)
		second.EXPECT().Close().Return().Once()
		sizes := make(chan [2]int, 4)
		factory := func(w, h int) (ports.Renderer, error) {
			sizes <- [2]int{w, h}
			if w == 1920 {
				return first, nil
			}
			return second, nil
		}
		host := New(set, ch, cursors.New(nil, 0), factory, logging.For(ctx, "virtuals"))
		want := []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "1920x1080"}}
		if err := host.Sync(ctx, want); err != nil {
			t.Fatal(err)
		}
		added, ok := (<-events).(ports.OutputAdded)
		if !ok || !added.Info.Virtual || added.Info.RefreshMilli != 60000 {
			t.Fatalf("added=%+v", added)
		}
		caps, ok := (<-events).(ports.OutputHDRCaps)
		if !ok || caps.Active {
			t.Fatalf("caps=%+v", caps)
		}
		if size := <-sizes; size != [2]int{1920, 1080} {
			t.Fatal(size)
		}
		registration, ok := (<-security).(ports.SecurityOutputAdded)
		if !ok || registration.Output != "tv" {
			t.Fatalf("registration=%+v", registration)
		}
		instance := set.Outs["tv"].Instance
		heads := host.Heads()
		if len(heads) != 1 || !heads[0].Enabled || !heads[0].Info.Virtual {
			t.Fatal(heads)
		}
		heads[0].Current.Width = 1
		heads[0].Modes[0].Height = 1
		if host.Heads()[0].Current.Width != 1920 || host.Heads()[0].Modes[0].Height != 1080 {
			t.Fatal("inventory aliases host")
		}
		if err := host.Sync(ctx, want); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(events) != 0 || len(security) != 0 || len(sizes) != 0 {
			t.Fatal("unchanged sync restarted owner")
		}
		want[0].Mode = "1280x720@30"
		if err := host.Sync(ctx, want); err != nil {
			t.Fatal(err)
		}
		changed, ok := (<-events).(ports.OutputAdded)
		if !ok || changed.Info.Width != 1280 || changed.Info.RefreshMilli != 30000 {
			t.Fatalf("changed=%+v", changed)
		}
		if size := <-sizes; size != [2]int{1280, 720} {
			t.Fatal(size)
		}
		synctest.Wait()
		if set.Outs["tv"].Instance != instance || len(security) != 0 {
			t.Fatal("mode change recreated lifetime")
		}
		if err := host.Sync(ctx, nil); err != nil {
			t.Fatal(err)
		}
		removed, ok := (<-events).(ports.OutputRemoved)
		if !ok || removed.Name != "tv" {
			t.Fatalf("removed=%+v", removed)
		}
		removal, ok := (<-security).(ports.SecurityOutputRemoved)
		if !ok || removal.Instance != registration.Instance {
			t.Fatalf("removal=%+v", removal)
		}
		if host.Owns("tv") || len(host.Heads()) != 0 || len(set.Outs) != 0 {
			t.Fatal("owner retained")
		}
		if err := host.Sync(ctx, nil); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(events) != 0 || len(security) != 0 {
			t.Fatal("duplicate removal")
		}
	})
}

func TestHostAnnouncedBeforeOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan ports.OutputEvent)
		ch := routing.Channels{Events: events}
		set := routing.NewSet(ctx, nil, ch.Routes())
		renderer := portsmocks.NewMockRenderer(t)
		renderer.EXPECT().Close().Return().Once()
		created := make(chan struct{}, 1)
		host := New(set, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { created <- struct{}{}; return renderer, nil }, logging.For(ctx, "virtuals"))
		done := make(chan error, 1)
		go func() { done <- host.Sync(ctx, []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "10x10"}}) }()
		synctest.Wait()
		if len(created) != 0 {
			t.Fatal("renderer created before announcement")
		}
		if _, ok := (<-events).(ports.OutputAdded); !ok {
			t.Fatal("first event not added")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if _, ok := (<-events).(ports.OutputHDRCaps); !ok {
			t.Fatal("second event not HDR caps")
		}
		<-created
		cancel()
		if err := set.Wait(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestParseMode(t *testing.T) {
	for _, tc := range []struct {
		value   string
		valid   bool
		refresh int
	}{
		{"10x10@0.0001", false, 0}, {"10x10@2147483.648", false, 0}, {"10x10@2147483.647", true, 2147483647}, {"10x10@0.0005", true, 1},
		{"10x10", true, 60000}, {"10x10@59.94", true, 59940}, {"preferred", false, 0}, {"0x10", false, 0}, {"16385x10", false, 0}, {"10x10@NaN", false, 0}, {"10x10@1e100", false, 0},
	} {
		t.Run(tc.value, func(t *testing.T) {
			mode, err := parseMode(tc.value)
			if (err == nil) != tc.valid || tc.valid && mode.RefreshMilli != tc.refresh {
				t.Fatalf("mode=%+v err=%v", mode, err)
			}
		})
	}
}

func TestModeAcknowledgementAndFailureRollback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan ports.OutputEvent, 32)
		ch := routing.Channels{Events: events}
		set := routing.NewSet(ctx, nil, ch.Routes())
		initial := portsmocks.NewMockRenderer(t)
		initial.EXPECT().Close().Return().Once()
		restored := portsmocks.NewMockRenderer(t)
		restored.EXPECT().Close().Return().Once()
		entered, release := make(chan struct{}), make(chan struct{})
		failure := errors.New("renderer allocation failed")
		creations := 0
		host := New(set, ch, cursors.New(nil, 0), func(w, h int) (ports.Renderer, error) {
			creations++
			if w == 1280 {
				close(entered)
				<-release
				return nil, failure
			}
			if creations == 1 {
				return initial, nil
			}
			return restored, nil
		}, logging.For(ctx, "virtuals"))
		file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "1920x1080"}}}
		apply := core.NewOutputApply(core.NewOutputOverrides(file, true))
		if err := host.Sync(ctx, apply.Virtuals()); err != nil {
			t.Fatal(err)
		}
		<-events
		<-events
		apply.Heads(domain.OutputHeads{Heads: host.Heads()})
		apply.Request(domain.OutputApply{ID: 7, Heads: []domain.HeadChange{{Name: "tv", Enabled: true, Custom: true, Mode: &domain.OutputMode{Width: 1280, Height: 720, RefreshMilli: 60000}}}})
		if _, ok := apply.Next(); !ok {
			t.Fatal("no apply")
		}
		done := make(chan error, 1)
		go func() { done <- host.Sync(ctx, apply.Virtuals()) }()
		<-entered
		synctest.Wait()
		if len(events) != 0 || host.Heads()[0].Info.Width != 1920 || len(done) != 0 {
			t.Fatal("mode published before owner acknowledgement")
		}
		close(release)
		err := <-done
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		apply.Finished(err)
		if _, ok := apply.Next(); !ok {
			t.Fatal("rollback not scheduled")
		}
		if err := host.Sync(ctx, apply.Virtuals()); err != nil {
			t.Fatal(err)
		}
		apply.Finished(nil)
		synctest.Wait()
		_, reply := apply.ReplyOut(make(chan domain.OutputApplied))
		if reply.ID != 7 || reply.Err == nil {
			t.Fatalf("reply %+v", reply)
		}
		if host.Heads()[0].Info.Width != 1920 {
			t.Fatal(host.Heads())
		}
		cancel()
		if err := set.Wait(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestModeWaitsForOwnerConsumption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan ports.OutputEvent, 16)
		ch := routing.Channels{Events: events}
		set := routing.NewSet(ctx, nil, ch.Routes())
		first, second := portsmocks.NewMockRenderer(t), portsmocks.NewMockRenderer(t)
		first.EXPECT().Close().Return().Once()
		second.EXPECT().Close().Return().Once()
		entered, release := make(chan struct{}), make(chan struct{})
		host := New(set, ch, cursors.New(nil, 0), func(w, h int) (ports.Renderer, error) {
			if w == 1920 {
				close(entered)
				<-release
				return first, nil
			}
			return second, nil
		}, logging.For(ctx, "virtuals"))
		want := []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "1920x1080"}}
		if err := host.Sync(ctx, want); err != nil {
			t.Fatal(err)
		}
		<-events
		<-entered
		want[0].Mode = "1280x720"
		done := make(chan error, 1)
		go func() { done <- host.Sync(ctx, want) }()
		synctest.Wait()
		if len(done) != 0 || len(events) != 0 || host.Heads()[0].Info.Width != 1920 {
			t.Fatal("unconsumed mode was published")
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if host.Heads()[0].Info.Width != 1280 {
			t.Fatal(host.Heads())
		}
		cancel()
		if err := set.Wait(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestModeResultRetainedAfterOwnerFinished(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("renderer allocation failed")
		ack := make(chan error, 1)
		done := make(chan struct{})
		go func() { ack <- failure; close(done) }()
		// Both signals are ready before the host resumes its wait. Either select
		// branch must retain the same owner error, not replace it with "stopped".
		synctest.Wait()
		if err := awaitMode(context.Background(), done, ack, "tv"); !errors.Is(err, failure) {
			t.Fatalf("lost owner failure: %v", err)
		}
	})
}

func TestModeStoppedWithoutAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		if err := awaitMode(context.Background(), done, make(chan error, 1), "tv"); err == nil {
			t.Fatal("missing stopped error")
		}
	})
}
