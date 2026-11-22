package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func drainWarnings(errs chan error) []string {
	var out []string
	for {
		select {
		case err := <-errs:
			if w, ok := errors.AsType[domain.ConfigWarning](err); ok {
				out = append(out, w.Msg)
			}
		default:
			return out
		}
	}
}

// A drop.<output> edge the layout cannot do warns once the output appears,
// and only once: an unrelated reload or another output resends nothing.
func TestDropAxisWarningOnOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := scrollDefaults()
		cfg.Terminal.AutoOpen = "off"
		var p domain.DropPolicy
		p.SetEdge(domain.DropSideLeft, domain.DropWorkspace)
		cfg.Drop.Outputs = []domain.OutputDrop{{Output: "A", DropPolicy: p}}
		output := make(chan ports.OutputEvent)
		reload := make(chan domain.ConfigChanged)
		errs := make(chan error, 8)
		ch := core.Channels{Client: make(chan ports.ClientEvent), Input: make(chan ports.InputEvent), Output: output, Config: reload, ConfigErrors: errs,
			Commands: make(chan ports.ClientCommand, 4096), Scenes: make(chan []domain.Scene, 1)}
		c, err := core.New(cfg, ch, testOptions(t, core.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() { cancel(); <-done }()
		synctest.Wait()
		if got := drainWarnings(errs); len(got) != 0 {
			t.Fatalf("warned before the output: %q", got)
		}
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 1000, Height: 600}}
		synctest.Wait()
		got := drainWarnings(errs)
		if len(got) != 1 || !strings.HasPrefix(got[0], "drop.A.edge.left: ") {
			t.Fatalf("warnings %q", got)
		}
		next := cfg
		next.Layout.Gaps = cfg.Layout.Gaps + 2
		reload <- domain.ConfigChanged{Config: next}
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "B", Width: 1000, Height: 600}}
		synctest.Wait()
		if got := drainWarnings(errs); len(got) != 0 {
			t.Fatalf("resent: %q", got)
		}
		// A drop config change warns again.
		next.Drop.SetEdge(domain.DropSideBottom, domain.DropScroll)
		reload <- domain.ConfigChanged{Config: next}
		synctest.Wait()
		if got := drainWarnings(errs); len(got) != 2 {
			t.Fatalf("after a drop change: %q", got)
		}
	})
}
