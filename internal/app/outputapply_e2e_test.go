package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/ports"
	wlr "github.com/bnema/purego-libwayland/protocol/wlroutputmanagement"
	"github.com/bnema/wlturbo"
)

// managerEvents records zwlr_output_manager_v1 head and done events.
type managerEvents struct {
	wlturbo.BaseProxy
	c     *wlturbo.Display
	heads []uint32
	done  uint32
}

func (p *managerEvents) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case wlr.ZwlrOutputManagerV1EventHead:
		h := &ignoredEvents{c: p.c}
		h.SetID(e.Uint32())
		p.c.Context().Register(h)
		p.heads = append(p.heads, h.ID())
	case wlr.ZwlrOutputManagerV1EventDone:
		p.done = e.Uint32()
	}
}

// ignoredEvents drops the events of head, mode and configuration head objects.
type ignoredEvents struct {
	wlturbo.BaseProxy
	c *wlturbo.Display
}

func (p *ignoredEvents) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(wlr.ZwlrOutputHeadV1EventMode) {
		m := &ignoredEvents{c: p.c}
		m.SetID(e.Uint32())
		p.c.Context().Register(m)
	}
}

// configurationEvents records the result of a zwlr_output_configuration_v1.
type configurationEvents struct {
	wlturbo.BaseProxy
	result int // 0 pending, then 1 + the event opcode
}

func (p *configurationEvents) Dispatch(e *wlturbo.Event) { p.result = int(e.Opcode) + 1 }

// A wlr-output-management client goes through Wayland, the headless owner
// loop and core: an accepted scale reaches the scene, a disable is refused.
func TestHeadlessOutputManagementEndToEnd(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	scenes := make(chan []ports.Scene, 64)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, Config: config.Defaults(), NoTerminal: true, Sizes: [][2]int{{640, 480}}, testScenes: scenes})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not stop")
		}
	})
	// The first scene means the Wayland socket exists.
	scene := func(want func(ports.Scene) bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case set := <-scenes:
				if len(set) == 1 && want(set[0]) {
					return
				}
			case err := <-done:
				if err != nil && strings.Contains(strings.ToLower(err.Error()), "vulkan") {
					t.Skipf("Vulkan unavailable: %v", err)
				}
				t.Fatalf("app exited early: %v", err)
			case <-deadline:
				t.Fatal("scene missing")
			}
		}
	}
	scene(func(s ports.Scene) bool { return s.Scale == 1 })
	sockets, _ := filepath.Glob(filepath.Join(runtime, "wayland-*[0-9]"))
	if len(sockets) != 1 {
		t.Fatalf("sockets: %v", sockets)
	}
	c, err := wlturbo.Connect(sockets[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	g, ok := c.Registry().FindGlobal("zwlr_output_manager_v1")
	if !ok {
		t.Fatal("no zwlr_output_manager_v1")
	}
	id, err := c.Registry().BindID(g.Name, g.Interface, 4)
	if err != nil {
		t.Fatal(err)
	}
	manager := &managerEvents{c: c}
	manager.SetID(id)
	c.Context().Register(manager)
	roundtrip := func(until func() bool) {
		t.Helper()
		// The owner loops answer asynchronously: roundtrip until they do.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if until() {
				return
			}
		}
		t.Fatal("no answer")
	}
	// The inventory reaches Wayland through the owner loop's outbox.
	roundtrip(func() bool { return manager.done != 0 && len(manager.heads) == 1 })
	head, serial := manager.heads[0], manager.done
	configure := func(set func(cfg uint32)) int {
		t.Helper()
		cfg := &configurationEvents{}
		cfg.SetID(c.AllocateID())
		c.Context().Register(cfg)
		if err := c.SendRequest(id, uint16(wlr.ZwlrOutputManagerV1RequestCreateConfiguration), cfg.ID(), manager.done); err != nil {
			t.Fatal(err)
		}
		set(cfg.ID())
		if err := c.SendRequest(cfg.ID(), uint16(wlr.ZwlrOutputConfigurationV1RequestApply)); err != nil {
			t.Fatal(err)
		}
		roundtrip(func() bool { return cfg.result != 0 })
		return cfg.result - 1
	}
	got := configure(func(cfg uint32) {
		ch := &ignoredEvents{c: c}
		ch.SetID(c.AllocateID())
		c.Context().Register(ch)
		if err := c.SendRequest(cfg, uint16(wlr.ZwlrOutputConfigurationV1RequestEnableHead), ch.ID(), head); err != nil {
			t.Fatal(err)
		}
		if err := c.SendRequest(ch.ID(), uint16(wlr.ZwlrOutputConfigurationHeadV1RequestSetScale), int32(512)); err != nil {
			t.Fatal(err)
		}
	})
	if got != int(wlr.ZwlrOutputConfigurationV1EventSucceeded) {
		t.Fatalf("scale 2: event %d", got)
	}
	scene(func(s ports.Scene) bool { return s.Scale == 2 })
	// A headless output cannot be disabled. The new scale gives a new serial.
	roundtrip(func() bool { return manager.done != serial })
	got = configure(func(cfg uint32) {
		if err := c.SendRequest(cfg, uint16(wlr.ZwlrOutputConfigurationV1RequestDisableHead), head); err != nil {
			t.Fatal(err)
		}
	})
	if got != int(wlr.ZwlrOutputConfigurationV1EventFailed) {
		t.Fatalf("disable: event %d", got)
	}
}
