package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// A burst of scale changes saves only the last scale of each output, once.
func TestPersistScalesDebounces(t *testing.T) {
	store := portsmocks.NewMockOutputScaleStore(t)
	notes := portsmocks.NewMockNotifier(t)
	done := make(chan struct{}, 2)
	store.EXPECT().SaveOutputScale("DP-1", 1.25).Return(nil).Once()
	store.EXPECT().SaveOutputScale("DP-2", 2.0).Return(nil).Once()
	notes.EXPECT().Notify("DP-1 scale saved", "Scale 1.25 is kept after restart").Run(func(string, string) { done <- struct{}{} }).Once()
	notes.EXPECT().Notify("DP-2 scale saved", "Scale 2 is kept after restart").Run(func(string, string) { done <- struct{}{} }).Once()
	changes := make(chan ports.ScaleChanged)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { core.PersistScales(ctx, changes, store, notes, 20*time.Millisecond); close(stopped) }()
	for _, ev := range []ports.ScaleChanged{{Output: "DP-2", Scale: 1.5}, {Output: "DP-1", Scale: 1.25}, {Output: "DP-2", Scale: 2}} {
		changes <- ev
	}
	receive(t, done)
	receive(t, done)
	cancel()
	<-stopped
}

// A failed save is reported and not retried.
func TestPersistScalesReportsFailure(t *testing.T) {
	store := portsmocks.NewMockOutputScaleStore(t)
	notes := portsmocks.NewMockNotifier(t)
	done := make(chan struct{}, 1)
	store.EXPECT().SaveOutputScale("DP-1", 1.5).Return(errors.New("read-only file system")).Once()
	notes.EXPECT().Notify("DP-1 scale not saved", "read-only file system").Run(func(string, string) { done <- struct{}{} }).Once()
	changes := make(chan ports.ScaleChanged)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { core.PersistScales(ctx, changes, store, notes, time.Millisecond); close(stopped) }()
	changes <- ports.ScaleChanged{Output: "DP-1", Scale: 1.5}
	receive(t, done)
	cancel()
	<-stopped
}

// Scale binds report the new scale of the focused output; a bind at the end
// of the range changes nothing and reports nothing.
func TestScaleBindReportsScale(t *testing.T) {
	cfg := config.Defaults()
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	scales := make(chan ports.ScaleChanged, 8)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Input: input, Output: output, Commands: make(chan ports.ClientCommand, 64), Scenes: scenes, Scales: scales})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "DP-2", Width: 5120, Height: 2160}}
	scene(t, scenes)
	press := func(sym string, code uint32) {
		input <- ports.KeyEvent{Keysym: sym, Keycode: code, Mods: ports.ModSuper, Pressed: true}
		input <- ports.KeyEvent{Keysym: sym, Keycode: code, Mods: ports.ModSuper}
	}
	press("minus", 12) // already at 1
	press("equal", 13)
	if got := receive(t, scales); got.Output != "DP-2" || got.Scale <= 1 {
		t.Fatalf("%+v", got)
	}
	if len(scales) != 0 {
		t.Fatalf("scale-down at 1 reported %+v", <-scales)
	}
}
