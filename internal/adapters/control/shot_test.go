package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestShotPNG(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "inline"}[inline], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				requests := make(chan domain.CaptureRequest)
				done := make(chan domain.CaptureDone)
				dir := t.TempDir()
				s := NewShooter(requests, done, dir)
				go func() {
					q := <-requests
					if q.ID&ports.CaptureIDControl == 0 || q.MinSeq != 7 || q.Output != "A" || q.Stride != 4 || q.Format != 1 {
						t.Errorf("request: %+v", q)
					}
					copy(q.Mapped, []byte{3, 2, 1, 0})
					done <- domain.CaptureDone{ID: q.ID}
				}()
				got, err := s.Shoot(context.Background(), []domain.ShotRequest{{Output: "A", MinSeq: 7, Width: 1, Height: 1}}, inline)
				if err != nil || len(got) != 1 {
					t.Fatalf("%+v %v", got, err)
				}
				if got[0].Path != filepath.Join(dir, "7-A.png") || got[0].Seq != 7 {
					t.Fatal(got)
				}
				info, err := os.Stat(got[0].Path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("mode: %v %v", info, err)
				}
				data, err := os.ReadFile(got[0].Path)
				if err != nil {
					t.Fatal(err)
				}
				img, err := png.Decode(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				r, g, b, a := img.At(0, 0).RGBA()
				if r != 257 || g != 514 || b != 771 || a != 65535 {
					t.Fatalf("pixel: %d %d %d %d", r, g, b, a)
				}
				wire, err := json.Marshal(got[0])
				if err != nil {
					t.Fatal(err)
				}
				if inline {
					if !bytes.Equal(data, got[0].PNG) || !strings.Contains(string(wire), `"png":"iVBOR`) {
						t.Fatal(string(wire))
					}
				} else if got[0].PNG != nil || strings.Contains(string(wire), `"png"`) {
					t.Fatal(string(wire))
				}
			})
		})
	}
}

func TestShotFailureAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		requests := make(chan domain.CaptureRequest)
		done := make(chan domain.CaptureDone, 2)
		s := NewShooter(requests, done, t.TempDir())
		if _, err := s.Shoot(context.Background(), []domain.ShotRequest{{Raw: true}}, false); err == nil || err.Error() != "raw shots need --every-frame" {
			t.Fatal(err)
		}
		if _, err := s.Shoot(context.Background(), []domain.ShotRequest{{Output: "missing"}}, false); err == nil {
			t.Fatal("unknown output accepted")
		}
		failure := errors.New("output failed")
		go func() { q := <-requests; done <- domain.CaptureDone{ID: q.ID, Err: failure} }()
		if _, err := s.Shoot(context.Background(), []domain.ShotRequest{{Output: "A", Width: 1, Height: 1}}, false); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { q := <-requests; cancel(); done <- domain.CaptureDone{ID: q.ID} }()
		if _, err := s.Shoot(ctx, []domain.ShotRequest{{Output: "A", Width: 1, Height: 1}}, false); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		// A subsequent shot reaps any cancelled destination before its own reply.
		go func() { q := <-requests; copy(q.Mapped, []byte{0, 0, 0, 255}); done <- domain.CaptureDone{ID: q.ID} }()
		if _, err := s.Shoot(context.Background(), []domain.ShotRequest{{Output: "A", Width: 1, Height: 1}}, false); err != nil {
			t.Fatal(err)
		}
		if len(s.orphans) != 0 {
			t.Fatal("abandoned mapping not reaped")
		}
	})
}

func TestShotCancellationBeforePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		requests := make(chan domain.CaptureRequest)
		done := make(chan domain.CaptureDone, 1)
		dir := t.TempDir()
		s := NewShooter(requests, done, dir)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			q := <-requests
			done <- domain.CaptureDone{ID: q.ID}
			q = <-requests
			// Readback is complete, but publication must not proceed.
			copy(q.Mapped, []byte{3, 2, 1, 0})
			cancel()
			done <- domain.CaptureDone{ID: q.ID}
		}()
		got, err := s.Shoot(ctx, []domain.ShotRequest{
			{Output: "A", MinSeq: 1, Width: 1, Height: 1},
			{Output: "B", MinSeq: 2, Width: 1, Height: 1},
		}, true)
		if !errors.Is(err, context.Canceled) || len(got) != 1 || got[0].Output != "A" || len(got[0].PNG) == 0 {
			t.Fatalf("shots: %+v, error: %v", got, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != "1-A.png" {
			t.Fatalf("unexpected published or temporary files: %v, %v", entries, err)
		}
		if len(s.token) != 0 {
			t.Fatal("shooter token not released")
		}
		// Reap a completion that cancellation may have left pending, using
		// the existing ownership path without publishing another shot.
		go func() {
			q := <-requests
			done <- domain.CaptureDone{ID: q.ID, Err: errors.New("reap only")}
		}()
		_, _ = s.Shoot(context.Background(), []domain.ShotRequest{{Output: "C", Width: 1, Height: 1}}, false)
		if len(s.orphans) != 0 {
			t.Fatal("cancelled mapping not reaped")
		}
	})
}

func TestShotContextWriterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var buffer bytes.Buffer
	writer := shotContextWriter{ctx: ctx, buffer: &buffer}
	if n, err := writer.Write([]byte("PNG")); n != 3 || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
	cancel()
	if n, err := writer.Write([]byte("more")); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %d, %v", n, err)
	}
	if buffer.String() != "PNG" {
		t.Fatalf("write after cancellation: %q", buffer.String())
	}
}
