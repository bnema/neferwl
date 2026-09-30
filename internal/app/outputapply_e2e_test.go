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
	client "github.com/bnema/wlturbo/protocol/outputmanagement"
)

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
	// A stuck loop never answers the client: the watchdog stops the app,
	// which closes the socket, so a blocked Roundtrip fails instead of hanging.
	watchdog := time.AfterFunc(10*time.Second, cancel)
	exited := false
	t.Cleanup(func() {
		watchdog.Stop()
		cancel()
		if exited {
			return
		}
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
				exited = true
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
	manager := client.NewOutputManager(c.Context())
	var heads []*client.OutputHead
	var serial uint32
	manager.OnHead(func(head *client.OutputHead) { heads = append(heads, head) })
	manager.OnDone(func(value uint32) { serial = value })
	bindTestClient(t, c, client.OutputManagerInterface, 4, manager)
	roundtrip := func(until func() bool) {
		t.Helper()
		// The owner loops answer asynchronously: roundtrip until they do,
		// or until the watchdog stops the app.
		for !until() {
			if err := c.Roundtrip(); err != nil {
				t.Fatalf("no answer: %v", err)
			}
		}
	}
	// The inventory reaches Wayland through the owner loop's outbox.
	roundtrip(func() bool { return serial != 0 && len(heads) == 1 })
	head, initialSerial := heads[0], serial
	configure := func(set func(*client.OutputConfiguration) *client.OutputConfigurationHead) int {
		t.Helper()
		// Inventory and core layout are independent startup publications. A
		// done serial can become stale before create_configuration reaches the
		// server. Only protocol cancellation is retryable, never a failed apply.
		for attempt := 0; attempt < 3; attempt++ {
			requested := serial
			cfg, err := manager.CreateConfiguration(requested)
			if err != nil {
				t.Fatal(err)
			}
			result := -1
			cfg.OnSucceeded(func() { result = int(wlr.ZwlrOutputConfigurationV1EventSucceeded) })
			cfg.OnFailed(func() { result = int(wlr.ZwlrOutputConfigurationV1EventFailed) })
			cfg.OnCancelled(func() { result = int(wlr.ZwlrOutputConfigurationV1EventCancelled) })
			// A configuration cancelled on creation accepts only its destructor.
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			var child *client.OutputConfigurationHead
			if result < 0 {
				child = set(cfg)
				if err := cfg.Apply(); err != nil {
					t.Fatal(err)
				}
				roundtrip(func() bool { return result >= 0 })
			}
			// Configuration heads have no destructor request of their own.
			if err := cfg.Destroy(); err != nil {
				t.Fatal(err)
			}
			if child != nil {
				c.Context().Unregister(child)
			}
			if result != int(wlr.ZwlrOutputConfigurationV1EventCancelled) {
				return result
			}
			if serial == requested {
				t.Fatalf("configuration cancelled without a newer inventory serial: %d", requested)
			}
		}
		t.Fatal("output inventory kept changing across three configurations")
		return -1
	}
	got := configure(func(cfg *client.OutputConfiguration) *client.OutputConfigurationHead {
		ch, err := cfg.EnableHead(head)
		if err != nil {
			t.Fatal(err)
		}
		if err := ch.SetScale(wlturbo.NewFixed(2)); err != nil {
			t.Fatal(err)
		}
		return ch
	})
	if got != int(wlr.ZwlrOutputConfigurationV1EventSucceeded) {
		t.Fatalf("scale 2: event %d", got)
	}
	scene(func(s ports.Scene) bool { return s.Scale == 2 })
	roundtrip(func() bool { return serial != initialSerial })
	got = configure(func(cfg *client.OutputConfiguration) *client.OutputConfigurationHead {
		if err := cfg.DisableHead(head); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if got != int(wlr.ZwlrOutputConfigurationV1EventFailed) {
		t.Fatalf("disable: event %d", got)
	}
}
