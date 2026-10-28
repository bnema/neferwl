package hints

import (
	"image"
	"image/color"
	"os"
	"os/exec"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

// canvas is a synthetic grayscale band: bg everywhere, ink for text.
type canvas struct {
	g       *image.Gray
	bg, ink uint8
	s       int // scale
}

func newCanvas(w, h int, bg, ink uint8, s int) *canvas {
	g := image.NewGray(image.Rect(0, 0, w*s, h*s))
	for i := range g.Pix {
		g.Pix[i] = bg
	}
	return &canvas{g, bg, ink, s}
}

func (c *canvas) fill(x0, y0, x1, y1 int, v uint8) {
	for y := y0 * c.s; y < y1*c.s; y++ {
		for x := x0 * c.s; x < x1*c.s; x++ {
			c.g.SetGray(x, y, color.Gray{v})
		}
	}
}

// word draws a 10 px high word of x-height body at (x,y) with the given
// width: text-like noisy ink (a stem every 3 px) and optional descenders.
// It returns the OCR box (descenders excluded, as a box around the body).
func (c *canvas) word(x, y, w int, descender bool) OCRWord {
	for i := 0; i < w; i += 3 {
		c.fill(x+i, y, x+i+1, y+10, c.ink)
	}
	box := image.Rect(x*c.s, y*c.s, (x+w)*c.s, (y+10)*c.s)
	if descender {
		for i := 0; i < w; i += 6 {
			c.fill(x+i, y+10, x+i+1, y+13, c.ink)
		}
		box.Max.Y = (y + 13) * c.s
	}
	return OCRWord{Text: "word", Rect: box}
}

func (c *canvas) underline(x0, x1, y int) { c.fill(x0, y, x1, y+1, c.ink) }

func scanUnder(c *canvas, words ...OCRWord) []domain.HintTarget {
	markUnderlined(c.g, words)
	return textLinks(words, float64(c.s))
}

func TestUnderline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bg, ink uint8
	}{{"dark-bg", 10, 204}, {"light-bg", 245, 30}} {
		for _, s := range []int{1, 2} {
			t.Run(tc.name, func(t *testing.T) {
				mk := func() *canvas { return newCanvas(200, 60, tc.bg, tc.ink, s) }

				c := mk()
				w := c.word(10, 20, 40, false)
				c.underline(10, 50, 32)
				got := scanUnder(c, w)
				require.Len(t, got, 1)
				require.Equal(t, domain.HintLink, got[0].Kind)
				require.Equal(t, domain.HintSourceOCR, got[0].Source)
				require.Equal(t, float32(1), got[0].Score)
				require.Equal(t, domain.WindowID(0), got[0].Window)
				require.Equal(t, 10, got[0].Rect.X)

				c = mk()
				require.Empty(t, scanUnder(c, c.word(10, 20, 40, false)), "plain word")

				c = mk()
				require.Empty(t, scanUnder(c, c.word(10, 20, 40, true)), "descenders")

				c = mk()
				w = c.word(10, 20, 40, false)
				c.underline(0, 200, 32)
				require.Empty(t, scanUnder(c, w), "rule across")

				c = mk()
				w = c.word(10, 20, 40, false)
				c.fill(10, 24, 50, 25, c.ink)
				require.Empty(t, scanUnder(c, w), "strikethrough")

				// Two words with a continuous underline: one target.
				c = mk()
				a, b := c.word(10, 20, 40, false), c.word(56, 20, 40, false)
				c.underline(10, 96, 32)
				got = scanUnder(c, a, b)
				require.Len(t, got, 1)
				require.Equal(t, 10, got[0].Rect.X)
				require.Equal(t, 86, got[0].Rect.W)

				// A gap in the underline: two targets.
				c = mk()
				a, b = c.word(10, 20, 40, false), c.word(56, 20, 40, false)
				c.underline(10, 50, 32)
				c.underline(56, 96, 32)
				require.Len(t, scanUnder(c, a, b), 2)
			})
		}
	}
}

// A box border under a word, with the word a part of a table row, is not a
// link; neither is an underline that continues beyond into unrelated text.
func TestUnderlineRejectsBox(t *testing.T) {
	c := newCanvas(300, 60, 10, 204, 1)
	w := c.word(100, 20, 40, false)
	c.underline(20, 280, 32)
	c.fill(20, 10, 21, 33, 204)
	c.fill(279, 10, 280, 33, 204)
	require.Empty(t, scanUnder(c, w))
}

// The line must be thin.
func TestUnderlineRejectsBlock(t *testing.T) {
	c := newCanvas(100, 60, 10, 204, 1)
	w := c.word(10, 20, 40, false)
	c.fill(10, 31, 50, 36, 204)
	require.Empty(t, scanUnder(c, w))
}

func TestUnderlinedURLIsOneTextTarget(t *testing.T) {
	c := newCanvas(300, 60, 10, 204, 1)
	w := c.word(10, 20, 100, false)
	w.Text = "example.org"
	c.underline(10, 110, 32)
	got := scanUnder(c, w)
	require.Len(t, got, 1)
	require.Equal(t, domain.HintSourceOCR, got[0].Source)
	require.Equal(t, "https://example.org", got[0].URL)
}

func TestUnderlinedProseHasNoURL(t *testing.T) {
	c := newCanvas(300, 60, 10, 204, 1)
	w1 := c.word(10, 20, 40, false)
	w1.Text = "Click"
	w2 := c.word(60, 20, 40, false)
	w2.Text = "here"
	c.underline(10, 100, 32)
	got := scanUnder(c, w1, w2)
	require.Len(t, got, 1)
	require.Empty(t, got[0].URL)

	c = newCanvas(300, 60, 10, 204, 1)
	w1 = c.word(10, 20, 40, false)
	w1.Text = "Visit"
	w2 = c.word(58, 20, 80, false)
	w2.Text = "example.com"
	c.underline(10, 138, 32)
	words := []OCRWord{w1, w2}
	markUnderlined(c.g, words)
	got = underlineOnlyTargets(words) // the plain link scan would drop this group as a duplicate
	require.Len(t, got, 1)
	require.Empty(t, got[0].URL)
}

// ocrTestdata reads the words of a testdata image with the real tesseract.
func ocrTestdata(t *testing.T, name string) (*image.Gray, []OCRWord) {
	t.Helper()
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
	f, err := os.Open("testdata/" + name)
	require.NoError(t, err)
	defer f.Close()
	img, _, err := image.Decode(f)
	require.NoError(t, err)
	g := image.NewGray(img.Bounds())
	for y := 0; y < g.Rect.Dy(); y++ {
		for x := 0; x < g.Rect.Dx(); x++ {
			g.Set(x, y, img.At(x, y))
		}
	}
	words, _, err := readWords(t.Context(), g)
	require.NoError(t, err)
	return g, words
}

// headingRule finds the thin horizontal rule under the heading of
// browser.png: the first row, from y=150, with a 900 px run of non-white
// pixels. It returns the rule as a rect.
func headingRule(t *testing.T, g *image.Gray) image.Rectangle {
	t.Helper()
	for y := 150; y < 220; y++ {
		run, start := 0, 0
		for x := 0; x < g.Rect.Dx(); x++ {
			if g.GrayAt(x, y).Y < 240 {
				if run == 0 {
					start = x
				}
				run++
				if run >= 900 {
					return image.Rect(start, y, start+run, y+1)
				}
			} else {
				run = 0
			}
		}
	}
	t.Fatal("heading rule not found")
	return image.Rectangle{}
}

// browser.png has blue links and no underlined link. No underline-only
// targets are emitted, and the heading rule is not taken for an underline.
func TestUnderlineOnBrowserTestdata(t *testing.T) {
	g, words := ocrTestdata(t, "browser.png")
	rule := headingRule(t, g)
	require.Empty(t, underlineOnlyTargets(words))
	for _, tg := range textLinks(words, 1) {
		r := image.Rect(tg.Rect.X, tg.Rect.Y, tg.Rect.X+tg.Rect.W, tg.Rect.Y+tg.Rect.H)
		require.False(t, r.Overlaps(rule), "target %v overlaps the heading rule %v", tg.Rect, rule)
	}
}

// underline.png is a real render (Chromium) of a generic page: underlined
// links `demo`, `source` and `ermite-runner hosts`, a table with row rules
// and an input box, which must yield nothing.
func TestUnderlineOnRealRender(t *testing.T) {
	_, words := ocrTestdata(t, "underline.png")
	near := func(r domain.Rect, x0, y0, x1, y1 int) bool {
		const tol = 8
		abs := func(v int) int { return max(v, -v) }
		return abs(r.X-x0) <= tol && abs(r.Y-y0) <= tol && abs(r.X+r.W-x1) <= tol && abs(r.Y+r.H-y1) <= tol
	}
	got := underlineOnlyTargets(words)
	for _, want := range [][4]int{{148, 122, 196, 137}, {306, 126, 364, 137}, {122, 165, 294, 180}} {
		found := false
		for _, tg := range got {
			found = found || near(tg.Rect, want[0], want[1], want[2], want[3])
		}
		require.True(t, found, "no target near %v in %v", want, got)
	}
	// The table (y 215..335) and the input box (y 360..410) give nothing.
	for _, tg := range textLinks(words, 1) {
		require.False(t, tg.Rect.Y > 200 && tg.Rect.Y < 420, "unexpected target %v", tg.Rect)
	}
}

// A single glyph with a flat bottom stroke (8, L) is not underlined text.
func TestUnderlineIgnoresSingleGlyph(t *testing.T) {
	c := newCanvas(100, 60, 10, 204, 1)
	w := c.word(10, 20, 10, false)
	w.Text = "8"
	c.underline(10, 20, 32)
	require.Empty(t, scanUnder(c, w))
}

func underlineOnlyTargets(words []OCRWord) []domain.HintTarget {
	return underlinedLinks(words, nil, 1)
}

func TestSyntheticLinkTargets(t *testing.T) {
	for _, levels := range [][2]uint8{{245, 60}, {10, 150}} {
		for i, text := range []string{"https://example.org", "www.example.org", "example.org", "user@example.org", "mailto:user@example.org", "1.2.3", "main.go", "127.0.0.1", "Menu", "Button"} {
			c := newCanvas(200, 60, levels[0], levels[1], 1)
			w := c.word(20, 20, 100, false)
			w.Text = text
			got := scanUnder(c, w)
			want := i < 5
			require.Equal(t, want, len(got) == 1, text)
		}
	}
}

func TestUnderlineRejectsTightBoxAndMenu(t *testing.T) {
	for _, s := range []int{1, 2} {
		c := newCanvas(100, 60, 245, 30, s)
		w := c.word(20, 20, 40, false)
		c.underline(20, 60, 32)
		c.fill(20, 10, 21, 33, c.ink)
		c.fill(59, 10, 60, 33, c.ink)
		require.Empty(t, scanUnder(c, w), "tight box border")

		c = newCanvas(200, 100, 245, 30, s)
		a, b := c.word(20, 20, 40, false), c.word(20, 50, 40, false)
		a.Text, b.Text = "Open", "Save"
		c.underline(10, 190, 32)
		require.Empty(t, scanUnder(c, a, b), "menu separator")
	}
}

func BenchmarkUnderline(b *testing.B) {
	c := newCanvas(1000, 60, 245, 60, 1)
	var words []OCRWord
	for x := 10; x < 950; x += 60 {
		words = append(words, c.word(x, 20, 40, false))
		c.underline(x, x+40, 32)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		markUnderlined(c.g, words)
		_ = textLinks(words, 1)
	}
}

// Glyph width averages count runes, not bytes: accented words group under one
// underline exactly like their ASCII equivalents.
func TestUnderlineGroupsMultibyteWords(t *testing.T) {
	for _, text := range []string{"abcd", "éàüö"} {
		c := newCanvas(200, 60, 10, 204, 1)
		a, b := c.word(10, 20, 20, false), c.word(35, 20, 20, false)
		a.Text, b.Text = text, text
		c.underline(10, 55, 32)
		require.Len(t, scanUnder(c, a, b), 1, text)
	}
}

func TestUnderlinedMailtoGetsURL(t *testing.T) {
	c := newCanvas(200, 60, 245, 60, 1)
	w := c.word(20, 20, 100, false)
	w.Text = "mailto:user@example.org"
	got := scanUnder(c, w)
	require.Len(t, got, 1)
	require.Equal(t, "mailto:user@example.org", got[0].URL)
}
