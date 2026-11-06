package headlessbackend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

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
