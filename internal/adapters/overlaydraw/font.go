package overlaydraw

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
)

// fontState is the font every raster draws with; gen changes with it, so
// the caches holding its faces and rasters (Raster, Titles, Panel) and the
// renderer's copies (FontGen) drop them.
type fontState struct {
	font *opentype.Font
	gen  uint64
}

var (
	current  atomic.Pointer[fontState]
	fontGens atomic.Uint64

	builtinOnce sync.Once
	builtin     fontState
)

// builtinFont is Go Mono, generation 0; its font is nil if it does not
// parse.
func builtinFont() *fontState {
	builtinOnce.Do(func() { builtin.font, _ = opentype.Parse(gomono.TTF) })
	return &builtin
}

func currentFont() *fontState {
	if st := current.Load(); st != nil {
		return st
	}
	return builtinFont()
}

// FontGen identifies the current font: 0 for the built-in one, a new value
// for each font file loaded.
func FontGen() uint64 { return currentFont().gen }

// LoadFont makes the TrueType or OpenType font at path the font of all
// later text; an empty path is the built-in Go Mono. A file that does not
// read or parse also falls back to Go Mono, and is returned as the error.
// Safe for concurrent use with the rasters.
func LoadFont(path string) error {
	var f *opentype.Font
	var err error
	if path != "" {
		var data []byte
		if data, err = os.ReadFile(path); err == nil {
			f, err = opentype.Parse(data)
		}
		if err != nil {
			f, err = nil, fmt.Errorf("font %s: %w", path, err)
		}
	}
	if f == nil {
		// The built-in font, generation 0.
		current.Store(nil)
		return err
	}
	current.Store(&fontState{font: f, gen: fontGens.Add(1)})
	return err
}
