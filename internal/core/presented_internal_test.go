package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestPresentedSequenceDoesNotStepIdleOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c := newSizedCore(t, cfg)
		frames := make(chan domain.OutputFrame)
		outputs := make(chan ports.OutputEvent)
		c.ch.Frames, c.ch.Output = frames, outputs
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		outputs <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 100, Height: 80}}
		synctest.Wait()
		<-c.ch.Scenes
		for _, seq := range []uint64{7, 4, 9} {
			frames <- domain.OutputFrame{Output: "A", SceneSeq: seq}
			synctest.Wait()
			want := max(uint64(7), seq)
			if c.presented["A"] != want {
				t.Fatalf("presented %d, want %d", c.presented["A"], want)
			}
			select {
			case scene := <-c.ch.Scenes:
				t.Fatalf("idle report published animation work: %+v", scene)
			default:
			}
		}
	})
}
