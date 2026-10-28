package hints

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"hash/maphash"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
)

// OCRWord is a recognized word in physical output coordinates.
type OCRWord struct {
	Text       string          `json:"text"`
	Rect       image.Rectangle `json:"rect"`
	Confidence float64         `json:"confidence"`
	// Underlined is set when a thin line runs under the word. UnderR is the
	// right end of that line (crop-local), which can reach the neighbouring
	// words of one underlined link.
	Underlined bool `json:"underlined,omitempty"`
	UnderR     int  `json:"under_r,omitempty"`
	// Line identifies the Tesseract text line of the word within a scan of a
	// window: band<<16 | ordinal. Words of one line share it.
	Line int `json:"line"`
}

var languageWarning sync.Once
var failureWarning sync.Once

// languageRunner bounds the external language-discovery process independently
// of recognition, which has not started yet when discovery runs.
type languageRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type execLanguageRunner struct{}

func (execLanguageRunner) Output(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, binary, args...).Output()
}

const languageTimeout = 5 * time.Second

func ocrLanguage(ctx context.Context, binary string) (string, error) {
	return discoverLanguage(ctx, binary, execLanguageRunner{}, languageTimeout)
}

func discoverLanguage(ctx context.Context, binary string, runner languageRunner, timeout time.Duration) (string, error) {
	dir := os.Getenv("TESSDATA_PREFIX")
	if dir == "" {
		dir = "/usr/share/tessdata"
	}
	if _, err := os.Stat(filepath.Join(dir, "eng.traineddata")); err == nil {
		return "eng", nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := runner.Output(ctx, binary, "--list-langs")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		lang := strings.TrimSpace(line)
		if lang != "" && lang != "osd" {
			languageWarning.Do(func() {
				log := logging.For(ctx, "hints")
				log.Warn().Str("language", lang).Msg("English OCR data missing; install tesseract-data-eng")
			})
			return lang, nil
		}
	}
	return "", fmt.Errorf("no OCR language available")
}

func parseTSV(data []byte, offset image.Point) ([]OCRWord, error) {
	var words []OCRWord
	var last [3]int
	ord := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 12)
		if len(fields) != 12 || fields[0] != "5" {
			continue
		}
		var values [4]int
		for i := range values {
			v, err := strconv.Atoi(fields[6+i])
			if err != nil {
				return nil, fmt.Errorf("OCR TSV coordinate: %w", err)
			}
			values[i] = v
		}
		conf, err := strconv.ParseFloat(fields[10], 64)
		if err != nil {
			return nil, fmt.Errorf("OCR TSV confidence: %w", err)
		}
		if values[2] <= 0 || values[3] <= 0 || strings.TrimSpace(fields[11]) == "" {
			continue
		}
		var id [3]int
		for i := range id {
			v, err := strconv.Atoi(fields[2+i])
			if err != nil {
				return nil, fmt.Errorf("OCR TSV line number: %w", err)
			}
			id[i] = v
		}
		if ord == 0 || id != last {
			ord++
			last = id
		}
		r := image.Rect(values[0], values[1], values[0]+values[2], values[1]+values[3]).Add(offset)
		words = append(words, OCRWord{Text: fields[11], Rect: r, Confidence: conf, Line: ord})
	}
	return words, scanner.Err()
}

// ocrWorkers is the maximum number of bands recognized in parallel. 4 keeps
// the tesseract processes at about 60% of the memory of 8 for the same time.
var ocrWorkers = 4

// pngLevel is the compression of non-grayscale images piped to tesseract.
var pngLevel = png.BestSpeed

// rawPGM feeds grayscale images to tesseract as binary PGM instead of PNG.
var rawPGM = true

// ocrState is the tesseract installation, resolved once.
type ocrState struct {
	done   bool
	binary string
	lang   string
	err    error
}

func (s *ocrState) resolve(ctx context.Context) error {
	if !s.done {
		s.done = true
		if s.binary, s.err = exec.LookPath("tesseract"); s.err == nil {
			s.lang, s.err = ocrLanguage(ctx, s.binary)
		}
	}
	return s.err
}

// ocrReady resolves tesseract on the first call. When it is missing, OCR is
// disabled for the life of the process and the warning is logged once.
func ocrReady(ctx context.Context, st *ocrState) bool {
	if err := st.resolve(ctx); err != nil {
		failureWarning.Do(func() {
			log := logging.For(ctx, "hints")
			log.Warn().Err(err).Msg("OCR unavailable; install tesseract and tesseract-data-eng")
		})
		return false
	}
	return true
}

const (
	// bandContext is the extra pixel margin read above and below a band for context.
	bandContext = 40
	// smallTextHeight is the median word height (px) under which a band is re-read upscaled 2x.
	smallTextHeight = 12
)

// band is a horizontal slice of a window crop recognized by one tesseract
// process. It reads rows [top,bottom) and owns the words centred in
// [ownTop,ownBottom); the margin only supplies context.
type band struct {
	idx, n            int
	top, bottom       int
	ownTop, ownBottom int
}

func bandsOf(height int) []band {
	n := min(runtime.GOMAXPROCS(0), ocrWorkers, height)
	bands := make([]band, 0, max(n, 0))
	for i := range n {
		ownTop, ownBottom := i*height/n, (i+1)*height/n
		bands = append(bands, band{idx: i, n: n, top: max(0, ownTop-bandContext), bottom: min(height, ownBottom+bandContext), ownTop: ownTop, ownBottom: ownBottom})
	}
	return bands
}

// recognize reads the bands of jobs that missed the cache, in parallel.
func recognize(ctx context.Context, st *ocrState, jobs []*bandJob) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// One scan-wide pool: at most ocrWorkers tesseract processes run at once,
	// whatever the number of windows.
	sem := make(chan struct{}, max(ocrWorkers, 1))
	var wg sync.WaitGroup
	for _, j := range jobs {
		if j.hit {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			j.err = ctx.Err()
			continue
		}
		wg.Go(func() {
			defer func() { <-sem }()
			j.words, j.err = readBand(ctx, st, j)
		})
	}
	wg.Wait()
}

// readBand recognizes one band; the words are crop-local and owned by it.
func readBand(ctx context.Context, st *ocrState, j *bandJob) ([]OCRWord, error) {
	words, err := recognizeBand(ctx, st.binary, st.lang, j.gray)
	if err != nil {
		return nil, err
	}
	heights := make([]int, 0, len(words))
	for _, word := range words {
		if len(word.Text) >= 3 {
			heights = append(heights, word.Rect.Dy())
		}
	}
	sort.Ints(heights)
	if len(heights) > 0 && heights[len(heights)/2] < smallTextHeight {
		words, err = recognizeBand(ctx, st.binary, st.lang, upscaleBand(j.gray))
		if err != nil {
			return nil, err
		}
		for i := range words {
			r := words[i].Rect
			words[i].Rect = image.Rect(r.Min.X/2, r.Min.Y/2, (r.Max.X+1)/2, (r.Max.Y+1)/2)
		}
	}
	markUnderlined(j.gray, words)
	// TSV line numbers restart per band and per read.
	for i := range words {
		words[i].Line = j.key.band<<16 | words[i].Line
	}
	// ownLines returns a fresh slice holding only retained words, so the cache
	// never references the discarded overlap.
	return ownLines(words, j.ext.Min.Y, j.own), nil
}

// ownLines shifts band-local words by extMinY to crop coordinates and keeps
// the whole lines whose median word centre lies in own. Overlap supplies
// context, but each line belongs to exactly one band.
func ownLines(words []OCRWord, extMinY int, own [2]int) []OCRWord {
	centres := map[int][]int{}
	for _, w := range words {
		centres[w.Line] = append(centres[w.Line], (w.Rect.Min.Y+w.Rect.Max.Y)/2+extMinY)
	}
	owned := map[int]bool{}
	for line, c := range centres {
		sort.Ints(c)
		m := c[len(c)/2]
		owned[line] = m >= own[0] && m < own[1]
	}
	var out []OCRWord
	for _, w := range words {
		if owned[w.Line] {
			w.Rect = w.Rect.Add(image.Pt(0, extMinY))
			out = append(out, w)
		}
	}
	return out
}

// The link patterns compile on the first OCR link check, not at process
// start: with hint mode off nothing here costs memory or CPU.
var domainPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+([a-z]{2,24})(?::[0-9]+)?(?:[/?#]\S*)?$`)
})
var schemePattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)^(?:https?://|(?:file|ftp):/{1,2})\S+$`)
})
var emailPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)^[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@(?:[a-z0-9-]+\.)+[a-z]{2,24}$`)
})

// fileExtensions lists the final dot-separated labels that name files, not
// domains. A bare "main.go" or "README.md" is syntactically a valid
// domain.tld, so these labels are rejected to avoid hinting file names.
// Matching is case-insensitive.
var fileExtensions = map[string]bool{
	"go": true, "md": true, "txt": true, "toml": true, "json": true, "yaml": true,
	"yml": true, "rs": true, "py": true, "js": true, "ts": true, "sh": true,
	"c": true, "h": true, "png": true, "jpg": true, "log": true, "conf": true,
	"lock": true, "mod": true, "sum": true, "html": true, "css": true,
}

// linkTrim is the punctuation trimmed around a candidate link.
const linkTrim = "()[]<>.,;:'\""

// linkForm is the kind of link a candidate token looks like.
type linkForm int

const (
	formNone   linkForm = iota // not a link
	formScheme                 // http(s)://..., file:..., ftp:...
	formWWW                    // www.x
	formEmail                  // user@host.tld
	formMailto                 // mailto:user@host.tld
	formBare                   // domain.tld[/path]
)

// classifyLink decides which supported form text has, if any. Malformed
// schemes (https:/x, htps://x) are not links.
func classifyLink(text string) linkForm {
	switch {
	case schemePattern().MatchString(text):
		return formScheme
	case len(text) > 4 && strings.EqualFold(text[:4], "www."):
		return formWWW
	case len(text) > 7 && strings.EqualFold(text[:7], "mailto:"):
		return formMailto
	case emailPattern().MatchString(text):
		return formEmail
	}
	if m := domainPattern().FindStringSubmatch(text); m != nil && !fileExtensions[strings.ToLower(m[1])] {
		return formBare
	}
	return formNone
}

// normalizeLink turns a classified candidate into the URL to open.
func normalizeLink(text string, form linkForm) string {
	switch form {
	case formScheme, formMailto:
		scheme, rest, _ := strings.Cut(text, ":")
		return strings.ToLower(scheme) + ":" + rest
	case formWWW, formBare:
		return "https://" + text
	case formEmail:
		return "mailto:" + text
	}
	return ""
}

// trimLink trims linkTrim punctuation around text, but keeps a trailing ")"
// that balances an opening one and a trailing "]" closing an IPv6 literal.
func trimLink(text string) string {
	text = strings.TrimLeft(text, linkTrim)
	for text != "" {
		last := text[len(text)-1]
		if strings.IndexByte(linkTrim, last) < 0 {
			break
		}
		if last == ')' && strings.Count(text, "(") >= strings.Count(text, ")") {
			break
		}
		if last == ']' && strings.Contains(text, "[") {
			break
		}
		text = text[:len(text)-1]
	}
	return text
}

// linkSpan is where trimLink's result sits in text.
func linkSpan(text string) (start, end int) {
	start = len(text) - len(strings.TrimLeft(text, linkTrim))
	return start, start + len(trimLink(text))
}

// linkURL reports whether text looks like a link and, when it does, the
// normalized URL to open; url is empty when the link is not openable.
func linkURL(text string) (url string, link bool) {
	start, end := linkSpan(text)
	text = text[start:end]
	form := classifyLink(text)
	if form == formNone {
		return "", false
	}
	url = normalizeLink(text, form)
	if !domain.ValidHintURL(url) {
		url = ""
	}
	return url, true
}

// touching reports whether word box b directly follows a on the same
// baseline, close enough to be part of the same token.
func touching(a, b image.Rectangle) bool {
	if b.Min.X < a.Max.X-2 {
		return false
	}
	if b.Min.X-a.Max.X > max(3, min(a.Dy(), b.Dy())/5) {
		return false
	}
	return sameBaseline(a, b, a.Dy())
}

// sameBaseline reports whether the bottoms of a and b are within the
// baseline tolerance for text of height h.
func sameBaseline(a, b image.Rectangle, h int) bool {
	return abs(b.Max.Y-a.Max.Y) <= max(3, h/3)
}

// logicalRect converts a pixel box to the enclosing logical rectangle.
func logicalRect(r image.Rectangle, scale float64) domain.Rect {
	x, y := int(math.Floor(float64(r.Min.X)/scale)), int(math.Floor(float64(r.Min.Y)/scale))
	right, bottom := int(math.Ceil(float64(r.Max.X)/scale)), int(math.Ceil(float64(r.Max.Y)/scale))
	return domain.Rect{X: x, Y: y, W: right - x, H: bottom - y}
}

// urlJoiners are the characters that tie OCR words of one link together even
// across a gap: Tesseract often splits "www . example . org" into words.
const urlJoiners = ".:/@"

// joinerToken reports whether s is made only of urlJoiners.
func joinerToken(s string) bool {
	return s != "" && strings.Trim(s, urlJoiners) == ""
}

// glued reports whether consecutive words a and b may belong to one link.
func glued(a, b string) bool {
	if joinerToken(a) || joinerToken(b) {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	la, _ := utf8.DecodeLastRuneInString(a)
	fb, _ := utf8.DecodeRuneInString(b)
	return strings.ContainsRune(urlJoiners, la) && strings.ContainsRune(urlJoiners, fb)
}

// linkRect is the box of the words of one link. Joiner tokens (dots) have
// unreliable heights, so the Y extent is clamped to the median-ish top and
// bottom of the other words.
func linkRect(words []OCRWord) image.Rectangle {
	var union image.Rectangle
	var tops, bottoms []int
	for _, w := range words {
		union = union.Union(w.Rect)
		if !joinerToken(w.Text) {
			tops = append(tops, w.Rect.Min.Y)
			bottoms = append(bottoms, w.Rect.Max.Y)
		}
	}
	if len(tops) == 0 {
		return union
	}
	sort.Ints(tops)
	sort.Ints(bottoms)
	r := union.Intersect(image.Rect(union.Min.X, tops[(len(tops)-1)/2], union.Max.X, bottoms[len(bottoms)/2]))
	if r.Empty() {
		return union
	}
	return r
}

func textLinks(words []OCRWord, scale float64) []domain.HintTarget {
	if scale <= 0 {
		scale = 1
	}
	var order []int
	lines := map[int][]OCRWord{}
	for _, w := range words {
		if _, ok := lines[w.Line]; !ok {
			order = append(order, w.Line)
		}
		lines[w.Line] = append(lines[w.Line], w)
	}
	var out []domain.HintTarget
	var urls []image.Rectangle // boxes of the URL targets, in pixels
	emit := func(group []OCRWord) bool {
		var text string
		var starts []int
		for _, w := range group {
			starts = append(starts, len(text))
			text += w.Text
		}
		url, link := linkURL(text)
		if !link {
			return false
		}
		start, end := linkSpan(text)
		var set []OCRWord
		for i, w := range group {
			if starts[i] < end && starts[i]+len(w.Text) > start {
				set = append(set, w)
			}
		}
		if len(set) == 0 {
			return false
		}
		rect := linkRect(set)
		out = append(out, domain.HintTarget{Kind: domain.HintLink, Source: domain.HintSourceOCR, Score: 1, URL: url, Rect: logicalRect(rect, scale)})
		urls = append(urls, rect)
		return true
	}
	for _, id := range order {
		lw := lines[id]
		for i := 0; i < len(lw); {
			k := i + 1
			for k < len(lw) && glued(lw[k-1].Text, lw[k].Text) {
				k++
			}
			run := lw[i:k]
			if !emit(run) && len(run) > 1 {
				for _, w := range run {
					emit([]OCRWord{w})
				}
			}
			i = k
		}
	}
	return append(out, underlinedLinks(words, urls, scale)...)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
func overlap(a, b image.Rectangle) float64 {
	intersection := a.Intersect(b)
	area := min(a.Dx()*a.Dy(), b.Dx()*b.Dy())
	if intersection.Empty() || area <= 0 {
		return 0
	}
	return float64(intersection.Dx()*intersection.Dy()) / float64(area)
}
func recognizeBand(ctx context.Context, binary, lang string, img image.Image) ([]OCRWord, error) {
	return recognizeImage(ctx, binary, lang, img, "11", false)
}

// pgmReader streams a grayscale image as binary PGM (P5), row by row.
type pgmReader struct {
	head []byte
	img  *image.Gray
	y    int
	row  []byte
}

func newPGMReader(img *image.Gray) *pgmReader {
	b := img.Bounds()
	return &pgmReader{head: fmt.Appendf(nil, "P5\n%d %d\n255\n", b.Dx(), b.Dy()), img: img}
}

func (r *pgmReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(r.head) > 0 {
			c := copy(p[n:], r.head)
			r.head, n = r.head[c:], n+c
			continue
		}
		if len(r.row) == 0 {
			b := r.img.Bounds()
			if r.y >= b.Dy() {
				break
			}
			r.row = r.img.Pix[(r.y+b.Min.Y-r.img.Rect.Min.Y)*r.img.Stride+(b.Min.X-r.img.Rect.Min.X):][:b.Dx()]
			r.y++
		}
		c := copy(p[n:], r.row)
		r.row, n = r.row[c:], n+c
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// A 5120x2160 screen holds well below 100,000 normal-sized words. 16 MiB
// allows over 160 TSV bytes per word, including hierarchy rows and text;
// 64 KiB is ample for tesseract diagnostics. Both limits are per band/run.
const ocrStdoutLimit = 16 << 20
const ocrStderrLimit = 64 << 10

// ocrOutput stops the child on overflow rather than draining unbounded output.
// Each stream has its own writer, owned by its exec copier until Wait returns.
type ocrOutput struct {
	buf      bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (b *ocrOutput) Write(p []byte) (int, error) {
	n := min(len(p), b.limit-b.buf.Len())
	_, _ = b.buf.Write(p[:n])
	if n < len(p) {
		b.exceeded = true
		b.cancel()
		return n, fmt.Errorf("OCR output exceeds %d bytes", b.limit)
	}
	return n, nil
}

func recognizeImage(ctx context.Context, binary, lang string, img image.Image, psm string, normalized bool) ([]OCRWord, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"stdin", "stdout", "--psm", psm, "-l", lang}
	if normalized {
		args = append(args, "-c", "tessedit_do_invert=0")
	}
	args = append(args, "tsv")
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	if g, ok := img.(*image.Gray); ok && rawPGM {
		// Raw grayscale: no deflate, no encoder buffers.
		cmd.Stdin = newPGMReader(g)
	} else {
		// The PNG streams into tesseract; no encoded copy is held.
		pr, pw := io.Pipe()
		defer func() { _ = pr.Close() }()
		go func() {
			enc := png.Encoder{CompressionLevel: pngLevel}
			_ = pw.CloseWithError(enc.Encode(pw, img))
		}()
		cmd.Stdin = pr
	}
	stdout := ocrOutput{limit: ocrStdoutLimit, cancel: cancel}
	stderr := ocrOutput{limit: ocrStderrLimit, cancel: cancel}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("tesseract: OCR output limit exceeded (stdout=%t, stderr=%t): %s", stdout.exceeded, stderr.exceeded, stderr.buf.String())
	}
	if err != nil {
		return nil, fmt.Errorf("tesseract: %w: %s", err, stderr.buf.String())
	}
	return parseTSV(stdout.buf.Bytes(), image.Point{})
}

// upscaleBand doubles a grayscale band with nearest-neighbour sampling.
func upscaleBand(src *image.Gray) *image.Gray {
	b := src.Bounds()
	dst := image.NewGray(image.Rect(0, 0, b.Dx()*2, b.Dy()*2))
	for y := 0; y < b.Dy(); y++ {
		row := src.Pix[(y+b.Min.Y-src.Rect.Min.Y)*src.Stride+(b.Min.X-src.Rect.Min.X):][:b.Dx()]
		top := dst.Pix[2*y*dst.Stride:][:dst.Stride]
		for x, v := range row {
			top[2*x], top[2*x+1] = v, v
		}
		copy(dst.Pix[(2*y+1)*dst.Stride:], top)
	}
	return dst
}

type bandJob struct {
	win   int             // index into the scan's windows
	key   bandKey         //
	ext   image.Rectangle // crop-local rows read, with context
	own   [2]int          // crop-local rows owned: words centred there are kept
	hash  uint64
	hit   bool
	gray  *image.Gray
	words []OCRWord
	err   error
}

func duplicateWord(words []OCRWord, word OCRWord) bool {
	for _, old := range words {
		if strings.EqualFold(word.Text, old.Text) && overlap(word.Rect, old.Rect) > .5 {
			return true
		}
	}
	return false
}

// bandKey identifies one OCR band of a window on an output.
type bandKey struct {
	output string
	window domain.WindowID
	band   int
	bands  int
}

// bandCache is the OCR result of a band whose pixels hash to hash. words are
// crop-local and only the ones the band owns.
type bandCache struct {
	hash  uint64
	ext   image.Rectangle
	words []OCRWord
}

// windowCrop rounds outwards in physical pixels, then intersects the output.
func windowCrop(r domain.Rect, scale float64, width, height int) image.Rectangle {
	if scale <= 0 {
		scale = 1
	}
	if r.W <= 0 || r.H <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(int(math.Floor(float64(r.X)*scale)), int(math.Floor(float64(r.Y)*scale)),
		int(math.Ceil(float64(r.X+r.W)*scale)), int(math.Ceil(float64(r.Y+r.H)*scale))).Intersect(image.Rect(0, 0, width, height))
}

// cropHash hashes the raw pixels of r without copying them.
func cropHash(seed maphash.Seed, buf []byte, stride int, r image.Rectangle) uint64 {
	var h maphash.Hash
	h.SetSeed(seed)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		_, _ = h.Write(buf[y*stride+r.Min.X*4 : y*stride+r.Max.X*4])
	}
	return h.Sum64()
}

// cropGray converts the BGRA pixels of r to 8-bit luma into dst, which must
// hold r.Dx()*r.Dy() bytes; only this band is converted, not the output.
func cropGray(buf []byte, stride int, r image.Rectangle, dst []byte) *image.Gray {
	img := &image.Gray{Pix: dst[:r.Dx()*r.Dy()], Stride: r.Dx(), Rect: image.Rect(0, 0, r.Dx(), r.Dy())}
	for y := 0; y < r.Dy(); y++ {
		src := buf[(r.Min.Y+y)*stride+r.Min.X*4:][:r.Dx()*4]
		row := img.Pix[y*img.Stride:][:r.Dx()]
		for x := range row {
			p := src[x*4 : x*4+4 : x*4+4]
			row[x] = uint8((uint32(p[2])*299 + uint32(p[1])*587 + uint32(p[0])*114 + 500) / 1000)
		}
	}
	return img
}
