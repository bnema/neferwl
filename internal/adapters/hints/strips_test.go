package hints

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"os/exec"
	"runtime"
	"sort"
	"sync"
	"time"
)

type stripOffset struct {
	source, packed image.Rectangle
	factor         int
}
type ocrStrip struct {
	img     *image.Gray
	offsets []stripOffset
}

func packLines(src *image.RGBA, lines []image.Rectangle, workers int, normalize bool) []ocrStrip {
	n := min(workers, len(lines))
	if n == 0 {
		return nil
	}
	groups := make([][]image.Rectangle, n)
	areas := make([]int, n)
	lines = append([]image.Rectangle(nil), lines...)
	sort.Slice(lines, func(i, j int) bool { return lines[i].Dx()*lines[i].Dy() > lines[j].Dx()*lines[j].Dy() })
	for _, r := range lines {
		best := 0
		for i := range n {
			if areas[i] < areas[best] {
				best = i
			}
		}
		groups[best] = append(groups[best], r)
		areas[best] += r.Dx() * r.Dy()
	}
	strips := make([]ocrStrip, n)
	for i, group := range groups {
		width, height := 0, 8
		for _, r := range group {
			factor := 1
			if r.Dy() < 12 {
				factor = 2
			}
			crop := r.Inset(-4).Intersect(src.Bounds())
			packed := image.Rect(4, height, 4+crop.Dx()*factor, height+crop.Dy()*factor)
			strips[i].offsets = append(strips[i].offsets, stripOffset{crop, packed, factor})
			width = max(width, packed.Max.X+4)
			height = packed.Max.Y + 8
		}
		dst := image.NewGray(image.Rect(0, 0, width, height))
		draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		for _, off := range strips[i].offsets {
			// The padded perimeter estimates the background without depending on text colour.
			var sum, count int
			r := off.source
			for x := r.Min.X; x < r.Max.X; x++ {
				sum += int(color.GrayModel.Convert(src.At(x, r.Min.Y)).(color.Gray).Y)
				count++
			}
			invert := normalize && count > 0 && sum/count < 128
			for y := off.packed.Min.Y; y < off.packed.Max.Y; y++ {
				for x := off.packed.Min.X; x < off.packed.Max.X; x++ {
					v := color.GrayModel.Convert(src.At(r.Min.X+(x-off.packed.Min.X)/off.factor, r.Min.Y+(y-off.packed.Min.Y)/off.factor)).(color.Gray).Y
					if invert {
						v = 255 - v
					}
					dst.SetGray(x, y, color.Gray{Y: v})
				}
			}
		}
		strips[i].img = dst
	}
	return strips
}

func readStrips(ctx context.Context, img *image.RGBA, lines []image.Rectangle, workers int, normalize bool) ([]OCRWord, int, error) {
	binary, err := exec.LookPath("tesseract")
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lang, err := ocrLanguage(ctx, binary)
	if err != nil {
		return nil, 0, err
	}
	strips := packLines(img, lines, min(workers, runtime.GOMAXPROCS(0), 16), normalize)
	results := make([][]OCRWord, len(strips))
	errs := make([]error, len(strips))
	var wg sync.WaitGroup
	for i, s := range strips {
		wg.Go(func() {
			words, e := recognizeImage(ctx, binary, lang, s.img, "6", normalize)
			errs[i] = e
			for _, w := range words {
				center := image.Pt((w.Rect.Min.X+w.Rect.Max.X)/2, (w.Rect.Min.Y+w.Rect.Max.Y)/2)
				for _, off := range s.offsets {
					if center.In(off.packed) {
						r := w.Rect.Sub(off.packed.Min)
						w.Rect = image.Rect(r.Min.X/off.factor, r.Min.Y/off.factor, (r.Max.X+off.factor-1)/off.factor, (r.Max.Y+off.factor-1)/off.factor).Add(off.source.Min)
						results[i] = append(results[i], w)
						break
					}
				}
			}
		})
	}
	wg.Wait()
	var words []OCRWord
	for i := range strips {
		if errs[i] != nil {
			return nil, len(strips), errs[i]
		}
		words = append(words, results[i]...)
	}
	return words, len(strips), nil
}
