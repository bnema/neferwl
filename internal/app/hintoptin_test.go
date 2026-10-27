package app

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
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

//testpolicy:realtime real headless session starts and joins hint workers
func TestHintOffStartsNoHintGoroutine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enable  bool
		sources []domain.HintSource
	}{
		{name: "off"},
		{name: "detector", enable: true, sources: []domain.HintSource{domain.HintSourceDetector}},
		{name: "atspi-ocr", enable: true, sources: []domain.HintSource{domain.HintSourceATSPI, domain.HintSourceOCR}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			cfg := config.Defaults()
			cfg.Hint.Enable, cfg.Hint.Sources = tc.enable, tc.sources
			// An unlisted detector must not be started, nor resolve this command.
			cfg.Hint.Detector = "/nonexistent/wp3-detector"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			scenes := make(chan []ports.Scene, 4096)
			go func() {
				done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, Config: cfg, NoTerminal: true, testScenes: scenes, Timeout: 800 * time.Millisecond})
			}()
			select {
			case <-scenes:
			case <-time.After(3 * time.Second):
				t.Fatal("no startup scene")
			}
			buf := make([]byte, 1<<20)
			dump := string(buf[:runtime.Stack(buf, true)])
			if !tc.enable {
				require.NotContains(t, dump, "adapters/hints.")
				require.NotContains(t, dump, "routeCaptured")
			}
			// Both constructors are lazy: no AT-SPI connect loop or detector child
			// before the first scan, including when the other source is listed.
			require.NotContains(t, dump, "adapters/atspi.")
			require.NotContains(t, dump, "(*procDetector).")
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("session did not stop")
			}
		})
	}
}

func TestHintOptionsOnlyConstructsListedATSPI(t *testing.T) {
	for _, sources := range [][]domain.HintSource{
		{domain.HintSourceDetector}, {domain.HintSourceATSPI, domain.HintSourceOCR},
	} {
		cfg := config.Defaults()
		cfg.Hint.Sources = sources
		opts := hintOptions(context.Background(), cfg)
		require.Equal(t, sources, opts.Sources)
		if sources[0] == domain.HintSourceDetector {
			require.Nil(t, opts.ATSPI)
		} else {
			require.NotNil(t, opts.ATSPI)
			require.NoError(t, opts.ATSPI.Close())
		}
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
	next.Hint.Sources = []domain.HintSource{domain.HintSourceDetector}
	next.Hint.LinksOnly = true
	next.Hint.TextClick = domain.ModAlt
	in <- ports.ConfigChanged{Config: next}
	got := (<-out).Config.Hint
	require.False(t, got.Enable)
	require.Equal(t, cur.Hint.Sources, got.Sources)
	require.True(t, got.LinksOnly)
	require.Equal(t, domain.ModAlt, got.TextClick)
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
