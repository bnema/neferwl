package headless

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
)

func TestContinuousCaptureStartStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		scenes := make(chan domain.Scene, 1)
		changes := make(chan ports.ContinuousCaptureRequest)
		done := make(chan error, 1)
		go func() {
			done <- RunOutput(ctx, Options{Width: 2, Height: 2, Continuous: changes, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, nil)
		}()
		dir := t.TempDir()
		ack := make(chan ports.ContinuousCaptureResult, 1)
		changes <- ports.ContinuousCaptureRequest{Dir: dir, Reply: ack}
		if result := <-ack; result.Err != "" {
			t.Fatal(result)
		}
		scenes <- domain.Scene{Seq: 1, Scale: 1, OutputWidth: 2, OutputHeight: 2}
		synctest.Wait()
		for _, name := range []string{"latest.png", "frame-000001.png", "frames.ffconcat"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		changes <- ports.ContinuousCaptureRequest{Stop: true, Reply: ack}
		<-ack
		scenes <- domain.Scene{Seq: 2, Scale: 1, OutputWidth: 2, OutputHeight: 2}
		synctest.Wait()
		if _, err := os.Stat(filepath.Join(dir, "frame-000002.png")); !os.IsNotExist(err) {
			t.Fatal("writer did not stop", err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
