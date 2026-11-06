package headlessbackend

import (
	"context"
	"github.com/stretchr/testify/mock"
	"image"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestVirtualReloadInventory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "10x10"}}}
		apply := core.NewOutputApply(core.NewOutputOverrides(file, true))
		events := make(chan ports.OutputEvent, 16)
		heads := make(chan domain.OutputHeads, 16)
		reload := make(chan domain.ConfigChanged)
		ch := routing.Channels{Events: events, Heads: heads, Reloads: reload}
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().Close().Return().Twice()
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, BackendOptions{Sizes: [][2]int{{20, 20}}}, apply, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { return r, nil }, logging.For(ctx, "render"))
		}()
		inventory := <-heads
		if len(inventory.Heads) != 2 || inventory.Heads[1].Info.Name != "tv" || !inventory.Heads[1].Info.Virtual {
			t.Fatal(inventory)
		}
		// Inventory is published before owners necessarily pass their announced
		// gates. Let both create their renderers before testing removal, otherwise
		// tv may correctly stop without ever owning a renderer to close.
		synctest.Wait()
		reload <- domain.ConfigChanged{Config: domain.Config{}}
		inventory = <-heads
		if len(inventory.Heads) != 1 {
			t.Fatal(inventory)
		}
		synctest.Wait()
		found := false
		for len(events) > 0 {
			if removed, ok := (<-events).(ports.OutputRemoved); ok && removed.Name == "tv" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing removal")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestVirtualOnlyNoFixedHeads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "10x10"}}}
		apply := core.NewOutputApply(core.NewOutputOverrides(file, true))
		events := make(chan ports.OutputEvent, 16)
		heads := make(chan domain.OutputHeads, 16)
		ch := routing.Channels{Events: events, Heads: heads, Reloads: make(chan domain.ConfigChanged)}
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().Close().Return().Maybe()
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, BackendOptions{}, apply, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { return r, nil }, logging.For(ctx, "render"))
		}()
		inventory := <-heads
		if len(inventory.Heads) != 1 || inventory.Heads[0].Info.Name != "tv" || !inventory.Heads[0].Info.Virtual {
			t.Fatal(inventory)
		}
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// The backend fans core's plan out to the output owners. A start that fails
// on one output stops those already started and reports the failure; a
// target gone since core planned is refused before anything starts.
func TestContinuousCapturePlan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		apply := core.NewOutputApply(core.NewOutputOverrides(domain.Config{}, true))
		continuous := make(chan ports.ContinuousCaptureRequest)
		heads := make(chan domain.OutputHeads, 16)
		ch := routing.Channels{Events: make(chan ports.OutputEvent, 16), Heads: heads, Reloads: make(chan domain.ConfigChanged), Continuous: continuous}
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().Close().Return().Maybe()
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, BackendOptions{Sizes: [][2]int{{20, 20}, {20, 20}}}, apply, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { return r, nil }, logging.For(ctx, "render"))
		}()
		inventory := <-heads
		synctest.Wait()
		first, second := inventory.Heads[0].Info.Name, inventory.Heads[1].Info.Name
		ask := func(plan ports.ContinuousCaptureRequest) ports.ContinuousCaptureResult {
			reply := make(chan ports.ContinuousCaptureResult, 1)
			plan.Reply = reply
			continuous <- plan
			return <-reply
		}
		dir := t.TempDir()
		if res := ask(ports.ContinuousCaptureRequest{Dir: dir, Targets: []ports.ContinuousTarget{{Output: "missing", Dir: dir}}}); res.Err != ports.ControlErrBadRequest {
			t.Fatalf("gone output: %+v", res)
		}
		// The second output's directory is a file: its start fails, and the
		// first, already started, is stopped again.
		if err := os.WriteFile(filepath.Join(dir, second), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		plan := ports.ContinuousCaptureRequest{Dir: dir, Targets: []ports.ContinuousTarget{{Output: first, Dir: filepath.Join(dir, first)}, {Output: second, Dir: filepath.Join(dir, second)}}}
		if res := ask(plan); res.Err != ports.ControlErrCaptureFailed || res.Dir != dir {
			t.Fatalf("partial start: %+v", res)
		}
		if err := os.Remove(filepath.Join(dir, second)); err != nil {
			t.Fatal(err)
		}
		if res := ask(plan); res.Err != "" || res.Dir != dir {
			t.Fatalf("start: %+v", res)
		}
		// Both outputs are recording. A failed replacement must preserve them.
		if res := ask(plan); res.Err != ports.ControlErrBadRequest {
			t.Fatalf("active recording replacement: %+v", res)
		}
		// Stop only the second: starting it first in a new plan must roll it
		// back when the already-active first target refuses, not stop first.
		if res := ask(ports.ContinuousCaptureRequest{Stop: true, Targets: []ports.ContinuousTarget{{Output: second}}}); res.Err != "" {
			t.Fatal(res)
		}
		reversed := plan
		reversed.Targets = []ports.ContinuousTarget{plan.Targets[1], plan.Targets[0]}
		if res := ask(reversed); res.Err != ports.ControlErrBadRequest {
			t.Fatalf("partial start with active recording: %+v", res)
		}
		if res := ask(ports.ContinuousCaptureRequest{Targets: []ports.ContinuousTarget{plan.Targets[0]}}); res.Err != ports.ControlErrBadRequest {
			t.Fatalf("active recording lost: %+v", res)
		}
		if res := ask(ports.ContinuousCaptureRequest{Targets: []ports.ContinuousTarget{plan.Targets[1]}}); res.Err != "" {
			t.Fatalf("new recording not rolled back: %+v", res)
		}
		if res := ask(ports.ContinuousCaptureRequest{Stop: true, Targets: []ports.ContinuousTarget{{Output: first}, {Output: second}}}); res.Err != "" {
			t.Fatalf("stop: %+v", res)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestBuiltinOutputRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		scenes := make(chan []domain.Scene)
		events := make(chan ports.OutputEvent, 16)
		reports := make(chan ports.OutputPresented, 8)
		sink := portsmocks.NewMockPresentationSink(t)
		sink.EXPECT().Push(mock.Anything).Run(func(p ports.OutputPresented) { reports <- p })
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().Close().Return()
		rendered := make(chan struct{}, 1)
		r.EXPECT().Render(mock.Anything, mock.Anything).Run(func(domain.Scene, map[domain.WindowID]domain.SurfaceContent) { rendered <- struct{}{} }).Return(nil, nil)
		r.EXPECT().TakeRedrawn().Return(0).Maybe()
		ch := routing.Channels{Events: events, Heads: make(chan domain.OutputHeads, 16), Scenes: scenes, Presented: sink}
		apply := core.NewOutputApply(core.NewOutputOverrides(domain.Config{}, true))
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, BackendOptions{Sizes: [][2]int{{20, 20}}}, apply, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { return r, nil }, logging.For(ctx, "render"))
		}()
		added := (<-events).(ports.OutputAdded)
		if added.Info.RefreshMilli != 60000 {
			t.Fatal(added.Info)
		}
		synctest.Wait()
		start := time.Now()
		scenes <- []domain.Scene{{Output: "HEADLESS-1", Seq: 1}}
		<-rendered
		period := time.Duration(16666667)
		if time.Since(start) != period {
			t.Fatalf("unpaced output: elapsed=%v, want %v", time.Since(start), period)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestConfigHDRRawCaptureWithoutGlobalFlag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "2x2", HDR: true}}}
		apply := core.NewOutputApply(core.NewOutputOverrides(file, true))
		continuous := make(chan ports.ContinuousCaptureRequest)
		scenes := make(chan []domain.Scene)
		heads := make(chan domain.OutputHeads, 16)
		ch := routing.Channels{Events: make(chan ports.OutputEvent, 16), Heads: heads, Scenes: scenes, Continuous: continuous}
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().Close().Return().Once()
		r.EXPECT().SetHDR(float64(203)).Return().Once()
		target, err := os.CreateTemp(t.TempDir(), "target")
		if err != nil {
			t.Fatal(err)
		}
		r.EXPECT().ExportTargets(1, []uint64(nil), false).Return([]domain.DMABuf{{Planes: []domain.DMABufPlane{{File: target}}}}, nil).Once()
		r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
		r.EXPECT().Pixels().Return(image.NewRGBA(image.Rect(0, 0, 2, 2)))
		read := make(chan struct{}, 1)
		r.EXPECT().HDRPixels().Run(func() { read <- struct{}{} }).Return(image.NewRGBA64(image.Rect(0, 0, 2, 2))).Once()
		r.EXPECT().TakeRedrawn().Return(0).Maybe()
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, BackendOptions{}, apply, ch, cursors.New(nil, 0), func(int, int) (ports.Renderer, error) { return r, nil }, logging.For(ctx, "render"))
		}()
		<-heads
		dir := t.TempDir()
		ack := make(chan ports.ContinuousCaptureResult, 1)
		continuous <- ports.ContinuousCaptureRequest{Dir: dir, Raw: true, Targets: []ports.ContinuousTarget{{Output: "tv", Dir: dir}}, Reply: ack}
		if result := <-ack; result.Err != "" {
			t.Fatal(result)
		}
		scenes <- []domain.Scene{{Output: "tv", Seq: 1, Scale: 1, OutputWidth: 2, OutputHeight: 2}}
		<-read
		synctest.Wait()
		if _, err := os.Stat(filepath.Join(dir, "latest-pq.png")); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
