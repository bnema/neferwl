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
	"sync"

	"github.com/bnema/neferwl/internal/adapters/logging"
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
	token    chan struct{}
	next     uint64
	orphans  map[uint64][]byte
}

// NewShooter writes into dir; each shot's pixel size comes from core.
func NewShooter(requests chan<- domain.CaptureRequest, done <-chan domain.CaptureDone, dir string) *Shooter {
	return &Shooter{requests: requests, done: done, dir: dir, token: make(chan struct{}, 1), orphans: make(map[uint64][]byte)}
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
		w, h := q.Width, q.Height
		const maxBytes = 512 * 1024 * 1024
		if w <= 0 || h <= 0 || w > maxBytes/4 || h > maxBytes/(w*4) {
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
			return results, errors.Join(ctx.Err(), unix.Munmap(pixels))
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
					if err := unix.Munmap(old); err != nil {
						log := logging.For(ctx, "control")
						log.Warn().Err(err).Uint64("capture", done.ID).Msg("unmap orphaned capture")
					}
					delete(s.orphans, done.ID)
				}
				if done.ID != id {
					continue
				}
				completed = true
				if done.Err != nil {
					return results, fmt.Errorf("capture %s: %w", q.Output, errors.Join(done.Err, unix.Munmap(pixels)))
				}
			case <-ctx.Done():
				s.orphans[id] = pixels
				return results, ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			return results, errors.Join(err, unix.Munmap(pixels))
		}
		// CaptureFrame.Read writes opaque little-endian XRGB (BGRA bytes):
		// swap to RGBA in the private mapping and encode it in place.
		for i := 0; i < len(pixels); i += 4 {
			pixels[i], pixels[i+2], pixels[i+3] = pixels[i+2], pixels[i], 255
		}
		img := &image.NRGBA{Pix: pixels, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
		var encoded bytes.Buffer
		err = errors.Join(shotPNG.Encode(&encoded, img), unix.Munmap(pixels))
		if err != nil {
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
			return results, errors.Join(err, os.Remove(f.Name()))
		}
		if err := os.Rename(f.Name(), path); err != nil {
			return results, errors.Join(err, os.Remove(f.Name()))
		}
		shot := Shot{Output: q.Output, Path: path, Seq: q.MinSeq}
		if inline {
			shot.PNG = encoded.Bytes()
		}
		results = append(results, shot)
	}
	return results, nil
}

// shotPNG matches the headless frame writer: fast compression, pooled
// encoder buffers.
var shotPNG = png.Encoder{CompressionLevel: png.BestSpeed, BufferPool: new(pngBuffers)}

// pngBuffers is a png.EncoderBufferPool.
type pngBuffers struct{ pool sync.Pool }

func (p *pngBuffers) Get() *png.EncoderBuffer {
	b, _ := p.pool.Get().(*png.EncoderBuffer)
	return b
}

func (p *pngBuffers) Put(b *png.EncoderBuffer) { p.pool.Put(b) }
