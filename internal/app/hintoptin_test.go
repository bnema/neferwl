package app

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/ports"
)

func TestHintPipesOffByDefault(t *testing.T) {
	require.False(t, config.Defaults().Hint.Enable)
	off := newPipes(false, false)
	require.Nil(t, off.hintScans)
	require.Nil(t, off.hintResults)
	require.Nil(t, off.hintCaptured)
	// No demultiplexing: the outputs reply on the wayland channel itself.
	require.Equal(t, off.captured, off.capturedAll)
	ch := off.core()
	require.Nil(t, ch.HintScans)
	require.Nil(t, ch.Hints)
	on := newPipes(false, true)
	require.NotNil(t, on.hintScans)
	require.NotNil(t, on.hintResults)
	require.NotNil(t, on.hintCaptured)
	require.NotEqual(t, on.captured, on.capturedAll)
}

func TestHintOffStartsNoHintGoroutine(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, Config: config.Defaults(), NoTerminal: true, Timeout: 800 * time.Millisecond})
	}()
	time.Sleep(400 * time.Millisecond)
	buf := make([]byte, 1<<20)
	dump := string(buf[:runtime.Stack(buf, true)])
	require.NotContains(t, dump, "adapters/hints.")
	require.NotContains(t, dump, "routeCaptured")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("session did not stop")
	}
}

func TestRelayConfigKeepsStartupHintEnable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cur := config.Defaults()
	in := make(chan ports.ConfigChanged, 1)
	out := make(chan ports.ConfigChanged, 1)
	go relayConfig(ctx, cur, in, out, make(chan *xkb.Keymap, 1), make(chan ports.InputDevicesConfig, 1), newCursors(clock.System{}, 0), make(chan ports.ClientCommand, 1), logging.For(ctx, "config"))
	next := config.Defaults()
	next.Hint.Enable = true
	next.Hint.Sources = []ports.HintSource{ports.HintSourceDetector}
	next.Hint.LinksOnly = true
	next.Hint.TextClick = ports.ModAlt
	in <- ports.ConfigChanged{Config: next}
	got := (<-out).Config.Hint
	require.False(t, got.Enable)
	require.Equal(t, cur.Hint.Sources, got.Sources)
	require.True(t, got.LinksOnly)
	require.Equal(t, ports.ModAlt, got.TextClick)
}

func TestRelayConfigKeepsStartupHintDetector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cur := config.Defaults()
	in := make(chan ports.ConfigChanged, 1)
	out := make(chan ports.ConfigChanged, 1)
	go relayConfig(ctx, cur, in, out, make(chan *xkb.Keymap, 1), make(chan ports.InputDevicesConfig, 1), newCursors(clock.System{}, 0), make(chan ports.ClientCommand, 1), logging.For(ctx, "config"))
	next := config.Defaults()
	next.Hint.Detector, next.Hint.Model, next.Hint.Threshold = "/other/nh", "/other/models", 0.5
	in <- ports.ConfigChanged{Config: next}
	got := (<-out).Config.Hint
	require.Equal(t, cur.Hint.Detector, got.Detector)
	require.Equal(t, cur.Hint.Model, got.Model)
	require.Equal(t, cur.Hint.Threshold, got.Threshold)
}
