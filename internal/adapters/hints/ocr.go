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
	"net"
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

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// OCRWord is a recognized word in physical output coordinates.
type OCRWord struct {
	Text       string          `json:"text"`
	Rect       image.Rectangle `json:"rect"`
	Confidence float64         `json:"confidence"`
	// Underlined is set when a thin line runs under the word. UnderL and
	// UnderR are the x extent of that line (crop-local), which can reach the
	// neighbouring words of one underlined link.
	Underlined bool `json:"underlined,omitempty"`
	UnderL     int  `json:"under_l,omitempty"`
	UnderR     int  `json:"under_r,omitempty"`
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
		r := image.Rect(values[0], values[1], values[0]+values[2], values[1]+values[3]).Add(offset)
		words = append(words, OCRWord{Text: fields[11], Rect: r, Confidence: conf})
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
		bands = append(bands, band{idx: i, n: n, top: max(0, ownTop-40), bottom: min(height, ownBottom+40), ownTop: ownTop, ownBottom: ownBottom})
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
	if len(heights) > 0 && heights[len(heights)/2] < 12 {
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
	owned := words[:0]
	for _, word := range words {
		// Overlap supplies context, but each word belongs to exactly one band.
		word.Rect = word.Rect.Add(image.Pt(0, j.ext.Min.Y))
		if center := (word.Rect.Min.Y + word.Rect.Max.Y) / 2; center >= j.own[0] && center < j.own[1] {
			owned = append(owned, word)
		}
	}
	return owned, nil
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
		jobs = append(jobs, &bandJob{ext: ext, own: [2]int{b.ownTop, b.ownBottom}, gray: img.SubImage(ext).(*image.Gray)})
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

// The link patterns compile on the first OCR link check, not at process
// start: with hint mode off nothing here costs memory or CPU.
var domainPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+([a-z]{2,24})(?::[0-9]+)?(?:[/?#]\S*)?$`)
})
var schemePattern = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)^(?:https?|htps|file|ftp):/{1,2}\S+$`) })
var emailPattern = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)^[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@(?:[a-z0-9-]+\.)+[a-z]{2,24}$`)
})
var fileExtensions = sync.OnceValue(func() []string {
	return strings.Fields("go md txt toml json yaml yml rs py js ts sh c h png jpg log conf lock mod sum html css")
})

func isTextLink(text string) bool {
	text = strings.Trim(text, "()[]<>.,;:'\"")
	if schemePattern().MatchString(text) || (strings.HasPrefix(strings.ToLower(text), "www.") && len(text) > 4) || emailPattern().MatchString(text) {
		return true
	}
	host := text
	if h, _, err := net.SplitHostPort(text); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		return true
	}
	m := domainPattern().FindStringSubmatch(text)
	if m == nil {
		return false
	}
	for _, ext := range fileExtensions() {
		if strings.EqualFold(m[1], ext) {
			return false
		}
	}
	return true
}

func textLinks(words []OCRWord, scale float64) []ports.HintTarget {
	if scale <= 0 {
		scale = 1
	}
	words = append([]OCRWord(nil), words...)
	sort.Slice(words, func(i, j int) bool {
		if words[i].Rect.Min.Y != words[j].Rect.Min.Y {
			return words[i].Rect.Min.Y < words[j].Rect.Min.Y
		}
		return words[i].Rect.Min.X < words[j].Rect.Min.X
	})
	var out []ports.HintTarget
	var urls []image.Rectangle // boxes of the URL targets, in pixels
	used := make([]bool, len(words))
	for i, word := range words {
		if used[i] {
			continue
		}
		text, rect := word.Text, word.Rect
		// Grow only touching words on the same baseline; ordinary prose is not joined.
		for {
			next := -1
			for j, candidate := range words {
				if j == i || used[j] || candidate.Rect.Min.X < rect.Max.X-2 {
					continue
				}
				gap := candidate.Rect.Min.X - rect.Max.X
				if gap > max(3, min(rect.Dy(), candidate.Rect.Dy())/5) {
					continue
				}
				if abs(candidate.Rect.Max.Y-rect.Max.Y) > max(3, rect.Dy()/3) {
					continue
				}
				if next < 0 || candidate.Rect.Min.X < words[next].Rect.Min.X {
					next = j
				}
			}
			if next < 0 {
				break
			}
			text += words[next].Text
			rect = rect.Union(words[next].Rect)
			used[next] = true
		}
		if !isTextLink(text) {
			continue
		}
		x, y := int(math.Floor(float64(rect.Min.X)/scale)), int(math.Floor(float64(rect.Min.Y)/scale))
		right, bottom := int(math.Ceil(float64(rect.Max.X)/scale)), int(math.Ceil(float64(rect.Max.Y)/scale))
		out = append(out, ports.HintTarget{Kind: ports.HintLink, Source: ports.HintSourceOCR, Score: 1, Rect: ports.Rect{X: x, Y: y, W: right - x, H: bottom - y}})
		urls = append(urls, rect)
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
func targetRect(t ports.HintTarget) image.Rectangle {
	return image.Rect(t.Rect.X, t.Rect.Y, t.Rect.X+t.Rect.W, t.Rect.Y+t.Rect.H)
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

func recognizeImage(ctx context.Context, binary, lang string, img image.Image, psm string, normalized bool) ([]OCRWord, error) {
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
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tesseract: %w: %s", err, stderr.String())
	}
	return parseTSV(output, image.Point{})
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
	window ports.WindowID
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
func windowCrop(r ports.Rect, scale float64, width, height int) image.Rectangle {
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

// targetSource reads accessibility targets; coverage belongs to the chain.
type targetSource interface {
	Targets(context.Context, []ports.HintWindow, bool) ([]ports.HintTarget, error)
	Close() error
}
