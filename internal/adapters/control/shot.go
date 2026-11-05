package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Shot is a file capture and, when requested, its PNG bytes (JSON base64).
type Shot struct {
	Output string `json:"output"`
	Path   string `json:"path"`
	Seq    uint64 `json:"seq"`
	PNG    []byte `json:"png,omitempty"`
}

// Shooter serializes captures and owns their destinations until CaptureDone.
// Its done channel must be dedicated to control captures. Cancellation retains
// an admitted mapping until a later Shoot reaps its matching completion.
type Shooter struct {
	requests chan<- domain.CaptureRequest
	done     <-chan domain.CaptureDone
	dir      string
	size     func(string) (int, int, bool)
	token    chan struct{}
	next     uint64
	orphans  map[uint64][]byte
}

// NewShooter uses size to resolve physical output pixel dimensions.
func NewShooter(requests chan<- domain.CaptureRequest, done <-chan domain.CaptureDone, dir string, size func(output string) (w, h int, ok bool)) *Shooter {
	return &Shooter{requests: requests, done: done, dir: dir, size: size, token: make(chan struct{}, 1), orphans: make(map[uint64][]byte)}
}

// Shoot captures presented scenes to private PNG files. Raw single shots are
// unsupported; raw PQ remains available through the every-frame writer.
func (s *Shooter) Shoot(ctx context.Context, shots []domain.ShotRequest, inline bool) ([]Shot, error) {
	select {
	case s.token <- struct{}{}:
		defer func() { <-s.token }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for _, q := range shots {
		if q.Raw {
			return nil, errors.New("raw shots need --every-frame")
		}
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	var results []Shot
	for _, q := range shots {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if s.size == nil {
			return results, errors.New("shot output size resolver is required")
		}
		w, h, ok := s.size(q.Output)
		const maxBytes = 512 * 1024 * 1024
		if !ok || w <= 0 || h <= 0 || w > maxBytes/4 || h > maxBytes/(w*4) {
			return results, fmt.Errorf("invalid or unknown shot output %q", q.Output)
		}
		if q.Output == "" || strings.ContainsAny(q.Output, "/\\") || q.Output == "." || q.Output == ".." {
			return results, fmt.Errorf("invalid shot output name %q", q.Output)
		}
		pixels, err := unix.Mmap(-1, 0, w*h*4, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
		if err != nil {
			return results, err
		}
		s.next++
		id := ports.CaptureIDControl | s.next
		request := domain.CaptureRequest{ID: id, Output: q.Output, MinSeq: q.MinSeq, Region: image.Rect(0, 0, w, h), Mapped: pixels, Width: w, Height: h, Stride: w * 4, Format: 1}
		select {
		case s.requests <- request:
		case <-ctx.Done():
			unix.Munmap(pixels)
			return results, ctx.Err()
		}
		completed := false
		for !completed {
			select {
			case done, open := <-s.done:
				if !open {
					s.orphans[id] = pixels
					return results, errors.New("control capture sink closed before completion")
				}
				if old, ok := s.orphans[done.ID]; ok {
					unix.Munmap(old)
					delete(s.orphans, done.ID)
				}
				if done.ID != id {
					continue
				}
				completed = true
				if done.Err != nil {
					unix.Munmap(pixels)
					return results, fmt.Errorf("capture %s: %w", q.Output, done.Err)
				}
			case <-ctx.Done():
				s.orphans[id] = pixels
				return results, ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			unix.Munmap(pixels)
			return results, err
		}
		// CaptureFrame.Read writes opaque little-endian XRGB (BGRA bytes).
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(pixels); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = pixels[i+2], pixels[i+1], pixels[i], 255
		}
		unix.Munmap(pixels)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, img); err != nil {
			return results, err
		}
		path := filepath.Join(s.dir, fmt.Sprintf("%d-%s.png", q.MinSeq, q.Output))
		f, err := os.CreateTemp(s.dir, ".shot-*.png")
		if err != nil {
			return results, err
		}
		_, writeErr := f.Write(encoded.Bytes())
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			os.Remove(f.Name())
			return results, err
		}
		if err := os.Rename(f.Name(), path); err != nil {
			os.Remove(f.Name())
			return results, err
		}
		shot := Shot{Output: q.Output, Path: path, Seq: q.MinSeq}
		if inline {
			shot.PNG = encoded.Bytes()
		}
		results = append(results, shot)
	}
	return results, nil
}
