package hints

import (
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

const (
	// maxDumps is the number of dump groups kept in the directory.
	maxDumps    = 50
	outlineSize = 2
)

// Options tunes Run.
type Options struct {
	// Command is the nefer-hints binary: a path, or a name looked up in PATH.
	Command string
	// ModelDir is the directory of the detector's model files.
	ModelDir string
	// Threshold is the minimum detection score, 0.01 to 0.99.
	Threshold float64
	// DumpDir, when not empty, receives the scanned frames (PNG, annotated
	// PNG and JSON). They contain the screen in clear.
	DumpDir string

	// detector replaces the subprocess in tests.
	detector detector
}

// dumpJob is one scan to write. pix is the capture buffer of the scan, owned
// by the job: writeDump converts it in place.
type dumpJob struct {
	when     time.Time
	req      ports.HintScanRequest
	pix      []byte
	m        *mapping // owns pix; released after the dump
	stride   int
	targets  []ports.HintTarget
	dets     []detection // detector boxes, physical pixels
	captureD time.Duration
	detectD  time.Duration
}

type dumpTarget struct {
	Rect  [4]int  `json:"rect"` // x1, y1, x2, y2, logical output-local
	Score float32 `json:"score"`
}

type dumpMeta struct {
	Output    string       `json:"output"`
	Width     int          `json:"width"`
	Height    int          `json:"height"`
	Stride    int          `json:"stride"`
	Scale     float64      `json:"scale"`
	CaptureMS float64      `json:"capture_ms"`
	DetectMS  float64      `json:"detect_ms"`
	TotalMS   float64      `json:"total_ms"`
	Targets   []dumpTarget `json:"targets"`
}

// boxColor is the outline of the detector boxes.
var boxColor = color.RGBA{0x00, 0xc8, 0x53, 0xff}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// writeDump writes the three files of j into dir, then prunes old groups.
func writeDump(dir string, j *dumpJob) error {
	if err := secureDir(dir); err != nil {
		return err
	}
	prefix := filepath.Join(dir, j.when.Format("20060102-150405.000")+"-"+safeName(j.req.Output))

	// The frame is converted in place to RGBA and encoded straight from the
	// capture buffer: no copy. The job owns the buffer, which is released
	// with it.
	for y := 0; y < j.req.Height; y++ {
		row := j.pix[y*j.stride : y*j.stride+j.req.Width*4]
		for x := 0; x < len(row); x += 4 {
			row[x], row[x+2], row[x+3] = row[x+2], row[x], 0xff
		}
	}
	img := &image.RGBA{Pix: j.pix, Stride: j.stride, Rect: image.Rect(0, 0, j.req.Width, j.req.Height)}
	if err := writePNG(prefix+".png", img); err != nil {
		return err
	}

	for _, d := range j.dets {
		outline(img, image.Rect(int(d.x1), int(d.y1), int(math.Ceil(float64(d.x2))), int(math.Ceil(float64(d.y2)))), boxColor)
	}
	if err := writePNG(prefix+".annotated.png", img); err != nil {
		return err
	}

	meta := dumpMeta{
		Output: j.req.Output, Width: j.req.Width, Height: j.req.Height, Stride: j.stride,
		Scale:     j.req.Scale,
		CaptureMS: ms(j.captureD), DetectMS: ms(j.detectD), TotalMS: ms(j.captureD + j.detectD),
		Targets: make([]dumpTarget, 0, len(j.targets)),
	}
	for _, t := range j.targets {
		meta.Targets = append(meta.Targets, dumpTarget{Rect: [4]int{t.Rect.X, t.Rect.Y, t.Rect.X + t.Rect.W, t.Rect.Y + t.Rect.H}, Score: t.Score})
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := writeFile(prefix+".json", func(f *os.File) error { _, err := f.Write(append(data, '\n')); return err }); err != nil {
		return err
	}
	return prune(dir, maxDumps)
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == 0 {
			return '_'
		}
		return r
	}, s)
}

func writePNG(path string, img image.Image) error {
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	return writeFile(path, func(f *os.File) error {
		bw := bufio.NewWriterSize(f, 1<<16)
		if err := enc.Encode(bw, img); err != nil {
			return err
		}
		return bw.Flush()
	})
}

// secureDir makes sure dir is a real directory owned by the current user and
// private to it, whatever it was before.
func secureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("dump dir %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("dump dir %s is not owned by the current user", dir)
	}
	return os.Chmod(dir, 0o700)
}

// writeFile fills a fresh private temporary file in the directory of path and
// renames it over path: an existing file or symlink there is replaced, never
// followed.
func writeFile(path string, fill func(*os.File) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".dump-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = f.Chmod(0o600)
	if err == nil {
		err = fill(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// outline draws a 2 px frame inside r, clipped to img.
func outline(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if x-r.Min.X < outlineSize || r.Max.X-1-x < outlineSize || y-r.Min.Y < outlineSize || r.Max.Y-1-y < outlineSize {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

// groupOf returns the "<timestamp>-<output>" prefix of a dump file name.
func groupOf(name string) string {
	for _, suf := range []string{".annotated.png", ".png", ".json"} {
		if strings.HasSuffix(name, suf) {
			return strings.TrimSuffix(name, suf)
		}
	}
	return ""
}

// prune keeps the keep most recent groups of dir; timestamps sort by name.
func prune(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	groups := map[string][]string{}
	for _, e := range entries {
		if g := groupOf(e.Name()); g != "" && !e.IsDir() {
			groups[g] = append(groups[g], e.Name())
		}
	}
	if len(groups) <= keep {
		return nil
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names[:len(names)-keep] {
		for _, f := range groups[g] {
			if err := os.Remove(filepath.Join(dir, f)); err != nil {
				return fmt.Errorf("prune: %w", err)
			}
		}
	}
	return nil
}
