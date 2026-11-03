package core

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"time"
)

func inputBenchCore(t testing.TB) *Core {
	t.Helper()
	cfg := domain.Config{}
	cfg.Layout.MaxColumns = 2
	cfg.Keyboard.CmdKey = "super"
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().Return(time.Unix(0, 0)).Maybe()
	c, err := New(cfg, Channels{Commands: make(chan ports.ClientCommand, 64), Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{Clock: clock}))
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(domain.OutputInfo{Name: "A", Width: 800, Height: 600})
	c.cursorX, c.cursorY = 10, 10
	return c
}

func BenchmarkInputDispatch(b *testing.B) {
	for _, devices := range []int{1, 2, 4} {
		for _, kind := range []string{"motion", "key"} {
			b.Run(fmt.Sprintf("%s/devices=%d", kind, devices), func(b *testing.B) {
				c := inputBenchCore(b)
				events := make([]ports.InputEvent, devices)
				for i := range events {
					if kind == "motion" {
						events[i] = ports.PointerMotion{X: 10, Y: 10}
					} else {
						events[i] = ports.KeyEvent{Keycode: 30, Keysym: "a"}
					}
				}
				configureBenchDevices(c, events)
				ctx := context.Background()
				for _, ev := range events {
					if err := c.handleInput(ctx, ev); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := c.handleInput(ctx, events[i%devices]); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestSingleDevicePointerMotionAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := inputBenchCore(t)
		events := []ports.InputEvent{ports.PointerMotion{X: 10, Y: 10}}
		configureBenchDevices(c, events)
		ctx := context.Background()
		if err := c.handleInput(ctx, events[0]); err != nil {
			t.Fatal(err)
		}
		if got := testing.AllocsPerRun(1000, func() {
			if err := c.handleInput(ctx, events[0]); err != nil {
				t.Fatal(err)
			}
		}); got != 0 {
			t.Fatalf("motion allocations=%v, origin/main baseline=0", got)
		}
	})
}

func BenchmarkConfiguredBindingDispatch(b *testing.B) {
	c := inputBenchCore(b)
	cfg := c.cfg
	cfg.Outputs = []domain.OutputConfig{{Name: "A", Inputs: []string{"benchmark"}}}
	if err := c.apply(cfg); err != nil {
		b.Fatal(err)
	}
	events := []ports.InputEvent{ports.PointerMotion{X: 10, Y: 10}}
	configureBenchDevices(c, events)
	ctx := context.Background()
	c.handleInput(ctx, events[0])
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.handleInput(ctx, events[0]); err != nil {
			b.Fatal(err)
		}
	}
}
