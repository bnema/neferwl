package hints

import (
	"context"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/bnema/neferwl/internal/ports"
)

func TestTextLink(t *testing.T) {
	yes := []string{"ermite.bnema.dev", "google.fr", "example.org/path?q=1", "https.google.fr", "https://example.org", "file:///tmp/main.go", "ftp://host/file", "www.main.go", "user@example.org", "127.0.0.1", "192.168.1.1:8080", "(https://example.org),", "htps://google.fr", "https:/example.org"}
	no := []string{"ermite-runner", "main.go", "README.md", "v1.2.3", "1.5", "e.g.", "report.txt", "source.rs", "alpha", "999.999.999.999"}
	for _, s := range yes {
		t.Run(s, func(t *testing.T) {
			if !isTextLink(s) {
				t.Errorf("not accepted: %s", s)
			}
		})
	}
	for _, s := range no {
		t.Run(s, func(t *testing.T) {
			if isTextLink(s) {
				t.Errorf("incorrectly accepted: %s", s)
			}
		})
	}
}
func TestParseTSV(t *testing.T) {
	data := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n5\t1\t1\t1\t1\t1\t10\t20\t30\t12\t91.5\texample.org\n"
	words, err := parseTSV([]byte(data), image.Pt(0, 40))
	if err != nil || len(words) != 1 {
		t.Fatalf("%v %v", words, err)
	}
	if words[0].Rect != image.Rect(10, 60, 40, 72) || words[0].Confidence != 91.5 {
		t.Fatal(words)
	}
	_, err = parseTSV([]byte(strings.ReplaceAll(data, "\t10\t", "\tbad\t")), image.Point{})
	if err == nil {
		t.Fatal("expected malformed coordinate error")
	}
}
func TestJoinedOCRLink(t *testing.T) {
	words := []OCRWord{{Text: "https:/", Rect: image.Rect(20, 20, 90, 40)}, {Text: "/example.org", Rect: image.Rect(91, 20, 200, 40)}}
	got := textLinks(words, 2)
	if len(got) != 1 || got[0].Source != ports.HintSourceOCR || got[0].Kind != ports.HintLink || got[0].Score != 1 || got[0].Rect != (ports.Rect{X: 10, Y: 10, W: 90, H: 10}) {
		t.Fatal(got)
	}
}
func TestRealOCR(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
	for _, name := range []string{"browser", "terminal"} {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open("testdata/" + name + ".png")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			src, err := png.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			img := image.NewRGBA(src.Bounds())
			draw.Draw(img, img.Bounds(), src, src.Bounds().Min, draw.Src)
			start := time.Now()
			words, bands, err := readWords(context.Background(), toGray(img))
			if err != nil {
				t.Fatal(err)
			}
			links := textLinks(words, 1)
			t.Logf("%s %dx%d ocr_ms=%.1f words=%d links=%d bands=%d", name, img.Bounds().Dx(), img.Bounds().Dy(), ms(time.Since(start)), len(words), len(links), bands)
			for _, w := range words {
				t.Logf("%s %v", w.Text, w.Rect)
			}
			if name == "terminal" {
				for _, r := range []image.Rectangle{image.Rect(160, 65, 380, 85), image.Rect(140, 90, 490, 110), image.Rect(95, 115, 630, 138), image.Rect(120, 140, 330, 163), image.Rect(385, 140, 740, 163)} {
					found := false
					for _, l := range links {
						if overlap(targetRect(l), r) > .5 {
							found = true
						}
					}
					if !found {
						t.Errorf("missing terminal URL near %v", r)
					}
				}
				if len(links) != 5 {
					t.Errorf("want 5 terminal links, got %d: %v", len(links), links)
				}
			} else if len(links) == 0 {
				t.Error("no browser links")
			}
		})
	}
}

func TestSmallGlyphOCR(t *testing.T) {
	binary, err := exec.LookPath("tesseract")
	if err != nil {
		t.Skip("tesseract not on PATH")
	}
	f, err := os.Open("testdata/terminal.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	small := image.NewGray(image.Rect(0, 0, 640, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 640; x++ {
			small.Set(x, y, src.At(x*2, y*2))
		}
	}
	lang, err := ocrLanguage(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, factor := range []int{1, 2} {
		var img image.Image = small
		if factor == 2 {
			img = upscaleBand(small)
		}
		start := time.Now()
		words, err := recognizeBand(context.Background(), binary, lang, img)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("glyph experiment %dx: %.1f ms, words=%d links=%d", factor, ms(time.Since(start)), len(words), len(textLinks(words, 1)))
	}
}

func TestPGMReader(t *testing.T) {
	g := image.NewGray(image.Rect(0, 0, 3, 3))
	for i := range g.Pix {
		g.Pix[i] = uint8(i + 1)
	}
	sub := g.SubImage(image.Rect(1, 1, 3, 3)).(*image.Gray)
	got, err := io.ReadAll(newPGMReader(sub))
	if err != nil {
		t.Fatal(err)
	}
	if want := "P5\n2 2\n255\n" + string([]byte{5, 6, 8, 9}); string(got) != want {
		t.Fatalf("%q", got)
	}
}

// Tesseract must read the raw PGM exactly as it reads the PNG.
func TestPGMMatchesPNG(t *testing.T) {
	binary, err := exec.LookPath("tesseract")
	if err != nil {
		t.Skip("tesseract not on PATH")
	}
	f, err := os.Open("testdata/terminal.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	rgba := image.NewRGBA(src.Bounds())
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	gray := toGray(rgba)
	lang, err := ocrLanguage(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	defer func(v bool) { rawPGM = v }(rawPGM)
	var res [2][]OCRWord
	for i, raw := range []bool{false, true} {
		rawPGM = raw
		if res[i], err = recognizeBand(context.Background(), binary, lang, gray); err != nil {
			t.Fatal(err)
		}
	}
	if len(res[0]) == 0 || !reflect.DeepEqual(res[0], res[1]) {
		t.Fatalf("PNG %d words, PGM %d words", len(res[0]), len(res[1]))
	}
}

// TestRecognizeCapsProcesses runs the bands of several windows through a
// stand-in tesseract that logs when each process starts and ends.
func TestRecognizeCapsProcesses(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	script := filepath.Join(dir, "tesseract")
	body := "#!/bin/sh\ncat >/dev/null\necho s >> " + logPath + "\nsleep 0.1\necho e >> " + logPath + "\nprintf 'level\\tpage_num\\tblock_num\\tpar_num\\tline_num\\tword_num\\tleft\\ttop\\twidth\\theight\\tconf\\ttext\\n'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	old := ocrWorkers
	ocrWorkers = 2
	t.Cleanup(func() { ocrWorkers = old })
	st := &ocrState{done: true, binary: script, lang: "eng"}
	var jobs []*bandJob
	for range 8 { // several windows' bands
		g := image.NewGray(image.Rect(0, 0, 8, 8))
		jobs = append(jobs, &bandJob{ext: g.Rect, own: [2]int{0, 8}, gray: g})
	}
	recognize(context.Background(), st, jobs)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cur, peak, starts := 0, 0, 0
	for _, l := range strings.Fields(string(data)) {
		if l == "s" {
			cur++
			starts++
			peak = max(peak, cur)
		} else {
			cur--
		}
	}
	if starts != 8 || peak > 2 {
		t.Fatalf("starts=%d peak=%d", starts, peak)
	}
	for _, j := range jobs {
		if j.err != nil {
			t.Fatal(j.err)
		}
	}
}

func TestLanguageDiscoveryTimeout(t *testing.T) {
	t.Setenv("TESSDATA_PREFIX", t.TempDir())
	runner := newMocklanguageRunner(t)
	parent := context.Background()
	runner.EXPECT().Output(mock.Anything, "tesseract", []string{"--list-langs"}).RunAndReturn(
		func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > languageTimeout {
				t.Fatal("language discovery has no bounded deadline")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}).Once()
	lang, err := discoverLanguage(parent, "tesseract", runner, 0)
	if lang != "" || !errors.Is(err, context.DeadlineExceeded) || parent.Err() != nil {
		t.Fatalf("language=%q error=%v parent=%v", lang, err, parent.Err())
	}
}

func TestLanguageDiscoveryFailureDisablesOCR(t *testing.T) {
	st := &ocrState{done: true, err: context.DeadlineExceeded}
	if ocrReady(context.Background(), st) {
		t.Fatal("failed discovery must disable OCR")
	}
	if err := st.resolve(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discovery failure was not retained: %v", err)
	}
}

// A real child process exercises exec's stream copiers and cancellation.
func TestOCROutputHelper(t *testing.T) {
	stream := os.Getenv("NEFERWL_OCR_OUTPUT_HELPER")
	if stream == "" {
		return
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(2)
	}
	out := os.Stdout
	if stream == "stderr" {
		out = os.Stderr
	}
	block := []byte(strings.Repeat("x\n", 4096)) // Short lines evade the TSV line limit.
	for {
		if _, err := out.Write(block); err != nil {
			os.Exit(0)
		}
	}
}

func TestRecognizeImageOutputLimits(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			t.Setenv("NEFERWL_OCR_OUTPUT_HELPER", stream)
			script := filepath.Join(t.TempDir(), "tesseract")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nexec '"+exe+"' -test.run='^TestOCROutputHelper$'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			words, err := recognizeImage(ctx, script, "eng", image.NewGray(image.Rect(0, 0, 8, 8)), "11", true)
			if err == nil || !strings.Contains(err.Error(), "OCR output limit exceeded") || len(words) != 0 {
				t.Fatalf("words=%v err=%v", words, err)
			}
			if ctx.Err() != nil {
				t.Fatalf("child was not stopped by output limit: %v", ctx.Err())
			}
			if len(err.Error()) > ocrStderrLimit+256 {
				t.Fatalf("unbounded diagnostic: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestOCROutputBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := ocrOutput{limit: 4, cancel: cancel}
	if n, err := out.Write([]byte("1234")); n != 4 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if ctx.Err() != nil {
		t.Fatal("exact limit canceled")
	}
	if n, err := out.Write([]byte("5")); n != 0 || err == nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if out.buf.String() != "1234" || !out.exceeded || ctx.Err() == nil {
		t.Fatal("overflow not bounded/canceled")
	}
}
