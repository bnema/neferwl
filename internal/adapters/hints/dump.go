package hints

import (
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
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

// dumpJob is one scan to write. pix is the capture buffer of the scan, owned
// by the job: writeDump converts it in place.
type dumpJob struct {
	stages   []scanStage
	when     time.Time
	req      ports.HintScanRequest
	pix      []byte
	m        *mapping // owns pix; released after the dump
	stride   int
	targets  []ports.HintTarget
	captureD time.Duration
}

type dumpTarget struct {
	Source string         `json:"source"`
	Kind   string         `json:"kind"`
	Window ports.WindowID `json:"window"`
	Rect   [4]int         `json:"rect"` // x1, y1, x2, y2, logical output-local
	Score  float32        `json:"score"`
}

type dumpMeta struct {
	Stages    []scanStage  `json:"stages"`
	Crops     []scanCrop   `json:"crops"`
	Sources   []string     `json:"sources"`
	LinksOnly bool         `json:"links_only"`
	Output    string       `json:"output"`
	Width     int          `json:"width"`
	Height    int          `json:"height"`
	Stride    int          `json:"stride"`
	Scale     float64      `json:"scale"`
	CaptureMS float64      `json:"capture_ms"`
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

	scale := j.req.Scale
	if scale <= 0 {
		scale = 1
	}
	for _, stage := range j.stages {
		for _, crop := range stage.Crops {
			outline(img, crop.Rect, color.RGBA{0x80, 0x80, 0x80, 0xff})
			for _, popup := range crop.Popups {
				outlineWidth(img, popup, color.RGBA{0x80, 0x80, 0x80, 0xff}, 1)
			}
		}
	}
	for _, t := range j.targets {
		r := image.Rect(int(float64(t.Rect.X)*scale), int(float64(t.Rect.Y)*scale), int(float64(t.Rect.X+t.Rect.W)*scale), int(float64(t.Rect.Y+t.Rect.H)*scale))
		outline(img, r, sourceColor(t.Source))
	}

	if err := writePNG(prefix+".annotated.png", img); err != nil {
		return err
	}

	meta := dumpMeta{
		Stages: j.stages, LinksOnly: j.req.LinksOnly,
		Output: j.req.Output, Width: j.req.Width, Height: j.req.Height, Stride: j.stride,
		Scale:     j.req.Scale,
		CaptureMS: ms(j.captureD), TotalMS: ms(j.captureD),
		Targets: make([]dumpTarget, 0, len(j.targets)),
	}
	for _, stage := range j.stages {
		meta.Sources = append(meta.Sources, stage.Source)
		meta.Crops = append(meta.Crops, stage.Crops...)
		meta.TotalMS += stage.MS
	}
	for _, t := range j.targets {
		kind := "button"
		if t.Kind == ports.HintLink {
			kind = "link"
		}
		meta.Targets = append(meta.Targets, dumpTarget{Source: t.Source.String(), Kind: kind, Window: t.Window, Rect: [4]int{t.Rect.X, t.Rect.Y, t.Rect.X + t.Rect.W, t.Rect.Y + t.Rect.H}, Score: t.Score})
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
	outlineWidth(img, r, c, outlineSize)
}

func outlineWidth(img *image.RGBA, r image.Rectangle, c color.RGBA, width int) {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if x-r.Min.X < width || r.Max.X-1-x < width || y-r.Min.Y < width || r.Max.Y-1-y < width {
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

// Source colours remain distinct even when sources report the same target kind.
func sourceColor(source ports.HintSource) color.RGBA {
	switch source {
	case ports.HintSourceATSPI:
		return color.RGBA{0xff, 0, 0xff, 0xff}
	case ports.HintSourceOCR:
		return color.RGBA{0, 0xb0, 0xff, 0xff}
	default:
		return boxColor
	}
}
