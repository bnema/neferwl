// Package xcursor loads cursor images from Xcursor themes in pure Go.
//
// File format: "Xcur" header, a table of contents, then image chunks of
// premultiplied ARGB8888 pixels at one or more nominal sizes.
package xcursor

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Image is one cursor frame: premultiplied ARGB8888, little-endian (B,G,R,A
// bytes), W*4 bytes per row. HotX/HotY is the click point.
// Nominal is the theme size the frame was made for (XCURSOR_SIZE units); the
// image itself may be larger (Breeze draws 32px for nominal 24).
type Image struct {
	W, H       int
	HotX, HotY int
	Nominal    int
	Pixels     []byte
}

const (
	chunkImage = 0xfffd0002
	maxSide    = 1024
)

// Decode returns the frame whose nominal size is the smallest one >= size,
// else the largest. Animated cursors return their first frame.
func Decode(data []byte, size int) (Image, error) {
	le := binary.LittleEndian
	if len(data) < 16 || string(data[:4]) != "Xcur" {
		return Image{}, errors.New("not an Xcursor file")
	}
	header, n := le.Uint32(data[4:]), le.Uint32(data[12:])
	if header < 16 || uint64(header)+uint64(n)*12 > uint64(len(data)) {
		return Image{}, errors.New("truncated table of contents")
	}
	best, bestSize := uint32(0), uint32(0)
	for i := uint32(0); i < n; i++ {
		e := data[header+i*12:]
		if le.Uint32(e) != chunkImage {
			continue
		}
		nominal, pos := le.Uint32(e[4:]), le.Uint32(e[8:])
		// Prefer the smallest nominal >= size; below size, the largest.
		better := bestSize == 0 ||
			(nominal >= uint32(size) && (bestSize < uint32(size) || nominal < bestSize)) ||
			(nominal < uint32(size) && bestSize < uint32(size) && nominal > bestSize)
		if better {
			best, bestSize = pos, nominal
		}
	}
	if bestSize == 0 {
		return Image{}, errors.New("no image chunk")
	}
	if uint64(best)+36 > uint64(len(data)) {
		return Image{}, errors.New("truncated image header")
	}
	h := data[best:]
	// The chunk header must repeat its table-of-contents entry.
	if le.Uint32(h) != 36 || le.Uint32(h[4:]) != chunkImage || le.Uint32(h[8:]) != bestSize {
		return Image{}, errors.New("image chunk does not match its table entry")
	}
	w, ht := le.Uint32(h[16:]), le.Uint32(h[20:])
	if w == 0 || ht == 0 || w > maxSide || ht > maxSide {
		return Image{}, fmt.Errorf("bad image size %dx%d", w, ht)
	}
	px := uint64(w) * uint64(ht) * 4
	if uint64(best)+36+px > uint64(len(data)) {
		return Image{}, errors.New("truncated pixels")
	}
	img := Image{Nominal: int(bestSize), W: int(w), H: int(ht), HotX: int(min(le.Uint32(h[24:]), w-1)), HotY: int(min(le.Uint32(h[28:]), ht-1))}
	img.Pixels = append([]byte(nil), h[36:36+px]...)
	return img, nil
}

// Scale resizes to w×h by averaging source pixels (box filter); averaging is
// correct on premultiplied alpha.
func (img Image) Scale(w, h int) Image {
	if w <= 0 || h <= 0 || (w == img.W && h == img.H) {
		return img
	}
	out := Image{Nominal: img.Nominal * w / img.W, W: w, H: h, HotX: img.HotX * w / img.W, HotY: img.HotY * h / img.H, Pixels: make([]byte, w*h*4)}
	for y := 0; y < h; y++ {
		y0, y1 := y*img.H/h, max((y+1)*img.H/h, y*img.H/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*img.W/w, max((x+1)*img.W/w, x*img.W/w+1)
			var sum [4]int
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					p := img.Pixels[(sy*img.W+sx)*4:]
					for c := range 4 {
						sum[c] += int(p[c])
					}
				}
			}
			n := (y1 - y0) * (x1 - x0)
			for c := range 4 {
				out.Pixels[(y*w+x)*4+c] = byte(sum[c] / n)
			}
		}
	}
	return out
}

// ForSize scales the image so its nominal size becomes size, keeping the
// theme's proportions, and never beyond limit pixels on a side.
func (img Image) ForSize(size, limit int) Image {
	nominal := max(img.Nominal, 1)
	w, h := img.W*size/nominal, img.H*size/nominal
	if big := max(w, h); big > limit {
		w, h = w*limit/big, h*limit/big
	}
	return img.Scale(max(w, 1), max(h, 1))
}

// SearchPath is XCURSOR_PATH or the usual icon directories.
func SearchPath() []string {
	if p := os.Getenv("XCURSOR_PATH"); p != "" {
		return filepath.SplitList(p)
	}
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		dirs = append(dirs, filepath.Join(data, "icons"), filepath.Join(home, ".icons"))
	}
	return append(dirs, "/usr/share/icons", "/usr/share/pixmaps")
}

// Load finds the named cursor in theme (following Inherits), falling back to
// the "default" and Adwaita themes, and decodes it at size.
func Load(dirs []string, theme string, names []string, size int) (Image, error) {
	seen := map[string]bool{}
	queue := []string{theme, "default", "Adwaita"}
	for len(queue) > 0 && len(seen) < 32 {
		t := queue[0]
		queue = queue[1:]
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		for _, dir := range dirs {
			for _, name := range names {
				data, err := os.ReadFile(filepath.Join(dir, t, "cursors", name))
				if err != nil {
					continue
				}
				if img, err := Decode(data, size); err == nil {
					return img, nil
				}
			}
		}
		queue = append(inherits(dirs, t), queue...)
	}
	return Image{}, fmt.Errorf("cursor %v not found in theme %q", names, theme)
}

// inherits reads Inherits= from the first index.theme of the theme.
func inherits(dirs []string, theme string) []string {
	for _, dir := range dirs {
		f, err := os.Open(filepath.Join(dir, theme, "index.theme"))
		if err != nil {
			continue
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		for s.Scan() {
			if k, v, ok := strings.Cut(s.Text(), "="); ok && strings.TrimSpace(k) == "Inherits" {
				return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })
			}
		}
		return nil
	}
	return nil
}
