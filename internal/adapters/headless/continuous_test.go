package headless

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"image"
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
		changes := make(chan ports.OutputContinuous)
		done := make(chan error, 1)
		go func() {
			done <- RunOutput(ctx, Options{Width: 2, Height: 2, Continuous: changes, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, nil)
		}()
		dir := t.TempDir()
		ack := make(chan ports.ContinuousCaptureResult, 1)
		changes <- ports.OutputContinuous{Dir: dir, Reply: ack}
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
		changes <- ports.OutputContinuous{Stop: true, Reply: ack}
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

// Starting a recording on an idle output writes its current frame at once:
// `shot --every-frame` then `--stop` with nothing changing still yields one.
func TestContinuousCaptureWritesIdleFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, f := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		scenes := make(chan domain.Scene, 1)
		changes := make(chan ports.OutputContinuous)
		done := make(chan error, 1)
		go func() {
			done <- RunOutput(ctx, Options{Width: 2, Height: 2, Continuous: changes, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, nil)
		}()
		scenes <- domain.Scene{Seq: 1, Scale: 1, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, f, 1)
		dir := t.TempDir()
		ack := make(chan ports.ContinuousCaptureResult, 1)
		changes <- ports.OutputContinuous{Dir: dir, Reply: ack}
		if result := <-ack; result.Err != "" {
			t.Fatal(result)
		}
		synctest.Wait()
		if _, err := os.Stat(filepath.Join(dir, "latest.png")); err != nil {
			t.Fatal("idle output wrote no frame:", err)
		}
		changes <- ports.OutputContinuous{Stop: true, Reply: ack}
		<-ack
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestContinuousCaptureWriteFailureKeepsOutputRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, frames := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		scenes := make(chan domain.Scene)
		changes := make(chan ports.OutputContinuous)
		done := make(chan error, 1)
		go func() {
			done <- RunOutput(ctx, Options{Width: 2, Height: 2, Continuous: changes, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, nil)
		}()
		ack := make(chan ports.ContinuousCaptureResult, 1)
		// /proc exists but cannot hold capture files, even when tests run as root.
		changes <- ports.OutputContinuous{Dir: "/proc", Reply: ack}
		if result := <-ack; result.Err != ports.ControlErrCaptureFailed {
			t.Fatalf("unwritable directory: %+v", result)
		}
		dir := t.TempDir()
		changes <- ports.OutputContinuous{Dir: dir, Reply: ack}
		if result := <-ack; result.Err != "" {
			t.Fatal(result)
		}
		// Fail the first write after successful admission, without permissions
		// depending on the user running the test.
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		scenes <- domain.Scene{Seq: 1, Scale: 1, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, frames, 1)
		synctest.Wait()
		// Recreate the directory: a disabled writer must not resume writing.
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		scenes <- domain.Scene{Seq: 2, Scale: 1, OutputWidth: 2, OutputHeight: 2}
		waitFrames(t, frames, 2)
		synctest.Wait()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatal("failed recording resumed", entries)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestContinuousRawHDRLiveTransition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sdr, _ := recordingRenderer(t, nil)
		hdr, _ := recordingRenderer(t, nil)
		target, err := os.CreateTemp(t.TempDir(), "hdr-target")
		if err != nil {
			t.Fatal(err)
		}
		hdr.EXPECT().SetHDR(float64(203)).Return().Once()
		hdr.EXPECT().ExportTargets(1, []uint64(nil), false).Return([]domain.DMABuf{{Planes: []domain.DMABufPlane{{File: target}}}}, nil).Once()
		hdr.EXPECT().HDRPixels().Return(image.NewRGBA64(image.Rect(0, 0, 2, 2))).Once()
		scenes := make(chan domain.Scene)
		changes := make(chan ports.OutputContinuous)
		modes := make(chan ModeRequest)
		done := make(chan error, 1)
		calls := 0
		go func() {
			done <- RunOutput(ctx, Options{Width: 2, Height: 2, Continuous: changes, ModeRequests: modes, NewRenderer: func(int, int) (ports.Renderer, error) {
				calls++
				if calls == 1 {
					return sdr, nil
				}
				return hdr, nil
			}}, scenes, nil, nil, nil, nil)
		}()
		dir := t.TempDir()
		ack := make(chan ports.ContinuousCaptureResult, 1)
		changes <- ports.OutputContinuous{Dir: dir, Raw: true, Reply: ack}
		if result := <-ack; result.Err != "" {
			t.Fatal(result)
		}
		scenes <- domain.Scene{Seq: 1, Scale: 1, OutputWidth: 2, OutputHeight: 2}
		synctest.Wait()
		if _, err := os.Stat(filepath.Join(dir, "latest-pq.png")); !os.IsNotExist(err) {
			t.Fatal("SDR wrote PQ", err)
		}
		reply := make(chan error, 1)
		settings := domain.OutputHDRSettings{Enabled: true}
		modes <- ModeRequest{HDR: &settings, Result: reply}
		if err := <-reply; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if _, err := os.Stat(filepath.Join(dir, "latest-pq.png")); err != nil {
			t.Fatal("live HDR wrote no PQ", err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
