package drm

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestCaptureCursorSnapshot(t *testing.T) {
	c := newCursor(nil, 64)
	c.image = true
	c.captureImage = domain.CursorImage{W: 1, H: 1, HotX: 2, Pixels: []byte{1, 2, 3, 255}}
	c.Move(10, 20)
	s := c.snapshot()
	if s == nil || s.X != 10 || s.Y != 20 || s.Image.HotX != 2 || s.Image.Pixels[2] != 3 {
		t.Fatalf("snapshot: %+v", s)
	}
	c.Hide()
	if c.snapshot() != nil {
		t.Fatal("away cursor captured")
	}
	c.Move(30, 40)
	c.image = false
	if c.snapshot() != nil {
		t.Fatal("security-invalidated cursor captured")
	}
}
