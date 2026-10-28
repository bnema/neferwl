package hints

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestTextLink(t *testing.T) {
	yes := []string{"ermite.bnema.dev", "google.fr", "example.org/path?q=1", "https.google.fr", "https://example.org", "file:///tmp/main.go", "ftp://host/file", "www.main.go", "user@example.org", "(https://example.org),"}
	no := []string{"127.0.0.1", "192.168.1.1:8080", "ermite-runner", "main.go", "README.md", "v1.2.3", "1.5", "e.g.", "report.txt", "source.rs", "alpha", "999.999.999.999", "htps://google.fr", "https:/example.org"}
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
func TestLinkURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		url  string
		link bool
	}{
		{"http://x.org", "http://x.org", true},
		{"HTTP://X.org", "http://X.org", true},
		{"https://x.org", "https://x.org", true},
		{"(https://x.org),", "https://x.org", true},
		{"https://en.wikipedia.org/wiki/Go_(programming_language)", "https://en.wikipedia.org/wiki/Go_(programming_language)", true},
		{"(https://x.org/a_(b)),", "https://x.org/a_(b)", true},
		{"http://[::1]:8080/", "http://[::1]:8080/", true},
		{"mailto:user@example.org", "mailto:user@example.org", true},
		{"MAILTO:user@example.org", "mailto:user@example.org", true},
		{"mailto:x@y.org?attach=/etc/passwd", "", true},
		{"www.x.org", "https://www.x.org", true},
		{"x.org", "https://x.org", true},
		{"example.com/p", "https://example.com/p", true},
		{"user@x.org", "mailto:user@x.org", true},
		{"https://x.org/a.", "https://x.org/a", true},
		{"file:///x", "", true},
		{"ftp://x", "", true},
		{"main.go", "", false},
		{"README.md", "", false},
		{"1.2.3", "", false},
		{"htps://x.org", "", false},
		{"https:/x.org", "", false},
	} {
		url, link := linkURL(tc.in)
		if link != tc.link || url != tc.url {
			t.Errorf("linkURL(%q) = %q, %t; want %q, %t", tc.in, url, link, tc.url, tc.link)
		}
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
	if len(got) != 1 || got[0].Source != domain.HintSourceOCR || got[0].Kind != domain.HintLink || got[0].Score != 1 || got[0].Rect != (domain.Rect{X: 10, Y: 10, W: 90, H: 10}) || got[0].URL != "https://example.org" {
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
	initial, err := recognizeBand(t.Context(), binary, lang, small)
	if err != nil {
		t.Fatal(err)
	}
	var heights []int
	for _, word := range initial {
		if len(word.Text) >= 3 {
			heights = append(heights, word.Rect.Dy())
		}
	}
	sort.Ints(heights)
	if len(heights) == 0 || heights[len(heights)/2] >= smallTextHeight {
		t.Fatalf("fixture must exercise small-glyph retry, heights=%v", heights)
	}
	want, err := recognizeBand(t.Context(), binary, lang, upscaleBand(small))
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("upscaled fixture yielded no words")
	}
	for i := range want {
		r := want[i].Rect
		want[i].Rect = image.Rect(r.Min.X/2, r.Min.Y/2, (r.Max.X+1)/2, (r.Max.Y+1)/2)
		if !want[i].Rect.In(small.Bounds()) {
			t.Fatalf("scaled word outside band: %v", want[i])
		}
	}
	markUnderlined(small, want)
	// A nonzero band origin must be applied after scaling, not halved.
	const top = 40
	for i := range want {
		want[i].Rect = want[i].Rect.Add(image.Pt(0, top))
	}
	got, err := readBand(t.Context(), &ocrState{done: true, binary: binary, lang: lang}, &bandJob{
		gray: small, ext: small.Bounds().Add(image.Pt(0, top)), own: [2]int{top, top + small.Bounds().Dy()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retry must use 2x words mapped back to crop coordinates:\ngot %v\nwant %v", got, want)
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

//testpolicy:realtime real OCR subprocess killed by the output limit, bounded by a context deadline
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

// readWords recognizes the words of img, a window crop, in parallel bands.
func readWords(ctx context.Context, img *image.Gray) ([]OCRWord, int, error) {
	st := &ocrState{}
	if err := st.resolve(ctx); err != nil {
		return nil, 0, err
	}
	var jobs []*bandJob
	for _, b := range bandsOf(img.Bounds().Dy()) {
		ext := image.Rect(0, b.top, img.Bounds().Dx(), b.bottom)
		jobs = append(jobs, &bandJob{key: bandKey{band: b.idx, bands: b.n}, ext: ext, own: [2]int{b.ownTop, b.ownBottom}, gray: img.SubImage(ext).(*image.Gray)})
	}
	recognize(ctx, st, jobs)
	var words []OCRWord
	for _, j := range jobs {
		if j.err != nil {
			return nil, len(jobs), j.err
		}
		for _, word := range j.words {
			if !duplicateWord(words, word) {
				words = append(words, word)
			}
		}
	}
	return words, len(jobs), nil
}

// isTextLink reports whether text looks like a link.
func isTextLink(text string) bool {
	_, link := linkURL(text)
	return link
}

func targetRect(t domain.HintTarget) image.Rectangle {
	return image.Rect(t.Rect.X, t.Rect.Y, t.Rect.X+t.Rect.W, t.Rect.Y+t.Rect.H)
}

// lineWords builds words of one Tesseract line from text and box pairs.
func lineWords(line int, items ...any) []OCRWord {
	var out []OCRWord
	for i := 0; i < len(items); i += 2 {
		out = append(out, OCRWord{Text: items[i].(string), Rect: items[i+1].(image.Rectangle), Line: line})
	}
	return out
}

// spread lays out whitespace-separated texts as 40px wide, 20px tall boxes
// 60px apart on line.
func spread(line int, texts ...string) []OCRWord {
	var out []OCRWord
	for i, s := range texts {
		out = append(out, OCRWord{Text: s, Rect: image.Rect(i*60, 10, i*60+40, 30), Line: line})
	}
	return out
}

func urlsOf(ts []domain.HintTarget) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.URL)
	}
	return out
}

func TestTextLinksSpacedDots(t *testing.T) {
	for _, tc := range []struct {
		name  string
		words []OCRWord
		urls  []string
	}{
		{"see main.go", spread(1, "see", "main.go", "and", "1.2.3"), nil},
		{"visit", spread(1, "Visit", "example.com"), []string{"https://example.com"}},
		{"Done.", spread(1, "Done.", "next"), nil},
		{"e.g.", spread(1, "e.g.", "foo"), nil},
		{"i.e.", spread(1, "i.e.", "the"), nil},
		{"slash", spread(1, "foo", "/", "example.com"), []string{"https://example.com"}},
		{"ellipsis", spread(1, "Loading", "...", "example.com"), []string{"https://example.com"}},
		{"mailto", spread(1, "Contact:", "user@example.org"), []string{"mailto:user@example.org"}},
		{"scheme gap", spread(1, "https:", "//example.org"), []string{"https://example.org"}},
		{"parens", spread(1, "(www", ".", "example", ".", "org),"), []string{"https://www.example.org"}},
		{"two urls", spread(1, "example.com", "and", "foo.org"), []string{"https://example.com", "https://foo.org"}},
		{"split lines", append(spread(1, "www", "."), spread(2, "example", ".", "org")...), []string{"https://example.org"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.urls, urlsOf(textLinks(tc.words, 1)))
		})
	}
}

func TestTextLinksBoxes(t *testing.T) {
	// Only the URL words get a box; the neighbours do not.
	got := textLinks(spread(1, "Visit", "example.com"), 1)
	require.Len(t, got, 1)
	require.Equal(t, domain.Rect{X: 60, Y: 10, W: 40, H: 20}, got[0].Rect)
	got = textLinks(spread(1, "Contact:", "user@example.org"), 1)
	require.Len(t, got, 1)
	require.Equal(t, domain.Rect{X: 60, Y: 10, W: 40, H: 20}, got[0].Rect)
	got = textLinks(spread(1, "foo", "/", "example.com"), 1)
	require.Len(t, got, 1)
	require.Equal(t, domain.Rect{X: 120, Y: 10, W: 40, H: 20}, got[0].Rect)
	got = textLinks(spread(1, "(www", ".", "example", ".", "org),"), 1)
	require.Len(t, got, 1)
	require.Equal(t, domain.Rect{X: 0, Y: 10, W: 280, H: 20}, got[0].Rect)
}

func TestTextLinksTesseractBoxes(t *testing.T) {
	r := image.Rect
	for _, tc := range []struct {
		name  string
		words []OCRWord
	}{
		{"B", lineWords(1, "www", r(0, 17, 43, 32), ".", r(54, 28, 58, 32), "example", r(66, 11, 238, 39), ".", r(175, 5, 189, 44), "org", r(195, 5, 241, 44))},
		{"B variant", lineWords(1, "www", r(0, 17, 43, 32), ".", r(54, 28, 58, 32), "example", r(66, 11, 238, 39), ".", r(177, 7, 186, 43), "org", r(192, 7, 240, 43))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := textLinks(tc.words, 1)
			require.Len(t, got, 1)
			require.Equal(t, "https://www.example.org", got[0].URL)
			x1 := 240
			if tc.name == "B" {
				x1 = 241
			}
			require.Equal(t, domain.Rect{X: 0, Y: 11, W: x1, H: 28}, got[0].Rect)
		})
	}
}

func TestParseTSVLineNumbers(t *testing.T) {
	row := func(b, p, l, w int, text string) string {
		return fmt.Sprintf("5\t1\t%d\t%d\t%d\t%d\t%d\t10\t20\t12\t90\t%s\n", b, p, l, w, w*30, text)
	}
	data := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		row(1, 1, 1, 1, "a") + row(1, 1, 1, 2, "b") + row(1, 1, 2, 1, "c") + row(2, 1, 1, 1, "d") + row(2, 1, 1, 2, "e")
	words, err := parseTSV([]byte(data), image.Point{})
	require.NoError(t, err)
	var lines []int
	for _, w := range words {
		lines = append(lines, w.Line)
	}
	require.Equal(t, []int{1, 1, 2, 3, 3}, lines)
}

func TestOwnLinesStraddlingBand(t *testing.T) {
	// Band-local words, extent starting at crop row 100; the band owns [100,140).
	words := []OCRWord{
		{Text: "a", Rect: image.Rect(0, 30, 10, 50), Line: 1}, // centre 40 -> 140
		{Text: "b", Rect: image.Rect(20, 0, 30, 20), Line: 1}, // centre 10 -> 110
		{Text: "c", Rect: image.Rect(40, 5, 50, 25), Line: 1}, // centre 15 -> 115
		{Text: "d", Rect: image.Rect(0, 60, 10, 80), Line: 2}, // 170
	}
	got := ownLines(words, 100, [2]int{100, 140})
	require.Len(t, got, 3)
	for _, w := range got {
		require.Equal(t, 1, w.Line)
	}
	require.Equal(t, image.Rect(0, 130, 10, 150), got[0].Rect)
	require.Len(t, ownLines(words, 100, [2]int{140, 200}), 1)
}

func TestRealSpacedDots(t *testing.T) {
	_, words := ocrTestdata(t, "spaced-dots.png")
	got := textLinks(words, 1)
	require.Equal(t, []string{"https://www.example.org"}, urlsOf(got), "words %v", words)
}
