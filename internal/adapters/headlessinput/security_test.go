package headlessinput

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

func TestSecureScriptSnapshotsEveryInputKind(t *testing.T) {
	km, err := xkb.New(xkb.RMLVO{Layout: "us"})
	if err != nil {
		t.Skip(err)
	}
	script := make(chan string, 4)
	for _, line := range []string{"key A", "move 10 20", "click", "swipe left"} {
		script <- line
	}
	close(script)
	input := make(chan ports.InputEvent, 32)
	security := portsmocks.NewMockSessionSecurity(t)
	state := ports.SecurityState{Generation: 7, Protected: true}
	security.EXPECT().Snapshot().Return(state)
	if err := RunSecure(context.Background(), km, nil, script, input, nil, nil, logging.For(context.Background(), "input"), security); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for len(input) > 0 {
		raw := <-input
		ev, ok := raw.(ports.SecurityInput)
		if !ok || ev.State != state {
			t.Fatalf("unstamped event %T %+v", raw, raw)
		}
		switch ev.Event.(type) {
		case ports.KeyEvent:
			kinds["key"] = true
		case ports.PointerMotion:
			kinds["motion"] = true
		case ports.PointerButton:
			kinds["button"] = true
		case ports.SwipeBegin:
			kinds["begin"] = true
		case ports.SwipeUpdate:
			kinds["update"] = true
		case ports.SwipeEnd:
			kinds["end"] = true
		default:
			t.Fatalf("unexpected input %T", ev.Event)
		}
	}
	if len(kinds) != 6 {
		t.Fatalf("input kinds %v", kinds)
	}
}

// securityLogBuffer is a real synchronized io.Writer, not a logging double.
type securityLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *securityLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *securityLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSecureScriptDiagnosticsRedactProtectedPayloads(t *testing.T) {
	lines := []string{
		"type 🔐", "key credential-secret", "key credential-modifier+A",
		"move credential-coordinate 2", "swipe credential-direction",
		"click credential-button", "sleep credential-duration", "credential-full-command",
	}
	for _, protected := range []bool{false, true} {
		name := "unlocked"
		if protected {
			name = "protected"
		}
		t.Run(name, func(t *testing.T) {
			km, err := xkb.New(xkb.RMLVO{Layout: "us"})
			if err != nil {
				t.Skip(err)
			}
			security := portsmocks.NewMockSessionSecurity(t)
			security.EXPECT().Snapshot().Return(ports.SecurityState{Generation: 1, Protected: protected}).Times(len(lines))
			var buf securityLogBuffer
			log := zerowrap.Logger{Logger: zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &buf}).With().Str("component", "input").Logger()}
			script := make(chan string, len(lines))
			for _, line := range lines {
				script <- line
			}
			close(script)
			if err := RunSecure(context.Background(), km, nil, script, make(chan ports.InputEvent, 32), nil, nil, log, security); err != nil {
				t.Fatal(err)
			}
			output := buf.String()
			entries := strings.Split(strings.TrimSpace(output), "\n")
			if len(entries) != len(lines) {
				t.Fatalf("diagnostics missing: %s", output)
			}
			if protected {
				for _, secret := range []string{"🔐", "credential", "type 🔐", "key credential-secret", "credential-full-command"} {
					if strings.Contains(output, secret) {
						t.Fatalf("protected credential %q logged: %s", secret, output)
					}
				}
				for _, entry := range entries {
					var fields map[string]any
					if err := json.Unmarshal([]byte(entry), &fields); err != nil {
						t.Fatal(err)
					}
					if fields["message"] != "invalid script input" {
						t.Fatalf("input-derived diagnostic: %s", entry)
					}
					for _, field := range []string{"rune", "key", "combo", "line"} {
						if _, exists := fields[field]; exists {
							t.Fatalf("protected field %s: %s", field, entry)
						}
					}
				}
			} else {
				for _, value := range []string{"🔐", "credential-secret", "credential-full-command", "unknown rune", "unknown key", "invalid move"} {
					if !strings.Contains(output, value) {
						t.Fatalf("unlocked diagnostic %q changed: %s", value, output)
					}
				}
			}
		})
	}
}

func TestSecureScriptSnapshotsBeforeKeyTranslation(t *testing.T) {
	km, err := xkb.New(xkb.RMLVO{Layout: "us"})
	if err != nil {
		t.Skip(err)
	}
	security := portsmocks.NewMockSessionSecurity(t)
	state := ports.SecurityState{Generation: 7, Protected: true}
	security.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState {
		// The first emitted event presses Shift. If snapshot happened after
		// km.Key, the live keymap would already contain the Shift modifier.
		if ev := km.Key(30, false, 0); ev.Mods&ports.ModShift != 0 {
			t.Error("snapshot taken after key translation updated Shift")
		}
		return state
	}).Once()
	security.EXPECT().Snapshot().Return(state).Times(3)
	script := make(chan string, 1)
	script <- "key Shift+A"
	close(script)
	input := make(chan ports.InputEvent, 4)
	if err := RunSecure(context.Background(), km, nil, script, input, nil, nil, logging.For(context.Background(), "input"), security); err != nil {
		t.Fatal(err)
	}
	if len(input) != 4 {
		t.Fatalf("events=%d", len(input))
	}
	first := (<-input).(ports.SecurityInput)
	if first.State != state || first.Event.(ports.KeyEvent).Keycode != 42 {
		t.Fatalf("first event %#v", first)
	}
}

func TestSecureScriptBlockedSendPreservesProductionSnapshot(t *testing.T) {
	for _, line := range []string{"key a", "move 1 2"} {
		t.Run(line, func(t *testing.T) {
			km, err := xkb.New(xkb.RMLVO{Layout: "us"})
			if err != nil {
				t.Skip(err)
			}
			original := ports.SecurityState{Generation: 1, Protected: true}
			changed := ports.SecurityState{Generation: 2}
			security := portsmocks.NewMockSessionSecurity(t)
			var current atomic.Pointer[ports.SecurityState]
			current.Store(&original)
			produced := make(chan struct{})
			security.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState {
				state := *current.Load()
				close(produced)
				return state
			}).Once()
			if line == "key a" {
				security.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return *current.Load() }).Once()
			}
			script := make(chan string, 1)
			script <- line
			close(script)
			input := make(chan ports.InputEvent) // no receiver until gate changes
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- RunSecure(ctx, km, nil, script, input, nil, nil, logging.For(ctx, "input"), security) }()
			select {
			case <-produced:
			case <-ctx.Done():
				t.Fatal("no production snapshot")
			}
			current.Store(&changed)
			select {
			case raw := <-input:
				if ev, ok := raw.(ports.SecurityInput); !ok || ev.State != original {
					t.Fatalf("blocked event restamped: %#v", raw)
				}
			case <-ctx.Done():
				t.Fatal("blocked event not delivered")
			}
			// The release of a key held across the transition is quarantined.
			// It must not be delivered into the new epoch.

			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("script did not stop")
			}
		})
	}
}

func TestSecureScriptQuarantinesModifierAcrossTransition(t *testing.T) {
	km, err := xkb.New(xkb.RMLVO{Layout: "us"})
	if err != nil {
		t.Fatal(err)
	}
	initial := ports.SecurityState{}
	locked := ports.SecurityState{Generation: 1, Protected: true}
	security := portsmocks.NewMockSessionSecurity(t)
	security.EXPECT().Snapshot().Return(initial).Once()  // Super down
	security.EXPECT().Snapshot().Return(locked).Times(5) // q down/up, held Super up, fresh q down/up
	script := make(chan string, 2)
	script <- "key Super+q"
	script <- "key q"
	close(script)
	input := make(chan ports.InputEvent, 8)
	if err := RunSecure(context.Background(), km, nil, script, input, nil, nil, logging.For(context.Background(), "input"), security); err != nil {
		t.Fatal(err)
	}
	if len(input) != 5 {
		t.Fatalf("quarantined release replayed: %d events", len(input))
	}
	first := (<-input).(ports.SecurityInput)
	if first.State != initial || first.Event.(ports.KeyEvent).Keycode != 125 {
		t.Fatalf("first event %#v", first)
	}
	for len(input) > 0 {
		ev := (<-input).(ports.SecurityInput)
		key := ev.Event.(ports.KeyEvent)
		if ev.State != locked || key.Keycode != 16 || key.Mods != 0 || key.State != (ports.ModState{}) {
			t.Fatalf("script password inherited modifiers: %#v", ev)
		}
	}
}
