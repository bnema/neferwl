package app

import (
	"context"
	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/ports"
	"testing"
	"time"
)

func TestQuitJoinsWorkers(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), Options{Backend: "headless", Config: config.Defaults()}, func(input chan<- ports.InputEvent) {
			input <- ports.KeyEvent{Keysym: "BackSpace", Mods: ports.ModCtrl | ports.ModAlt, Pressed: true}
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quit did not join workers")
	}
}
