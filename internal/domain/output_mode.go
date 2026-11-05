package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxOutputDim bounds the width and height of a virtual output mode.
const MaxOutputDim = 16384

// DefaultRefreshMilli is the refresh of a virtual mode written without @Hz.
const DefaultRefreshMilli = 60000

// ParseOutputMode parses a virtual output mode "WxH" or "WxH@Hz".
// Dimensions are 1..MaxOutputDim; the refresh defaults to 60 Hz and is
// rounded to mHz by RefreshMilli.
func ParseOutputMode(s string) (OutputMode, error) {
	size, rate, hasRate := strings.Cut(s, "@")
	ws, hs, ok := strings.Cut(size, "x")
	w, e1 := strconv.Atoi(ws)
	h, e2 := strconv.Atoi(hs)
	if !ok || e1 != nil || e2 != nil || w < 1 || h < 1 || w > MaxOutputDim || h > MaxOutputDim {
		return OutputMode{}, fmt.Errorf("invalid mode %q: want WxH[@Hz] with dimensions 1..%d", s, MaxOutputDim)
	}
	refresh := DefaultRefreshMilli
	if hasRate {
		hz, err := strconv.ParseFloat(rate, 64)
		milli, ok := RefreshMilli(hz)
		if err != nil || !ok {
			return OutputMode{}, fmt.Errorf("invalid refresh in %q: want 1..2147483647 mHz after rounding", s)
		}
		refresh = milli
	}
	return OutputMode{Width: w, Height: h, RefreshMilli: refresh}, nil
}

// FormatOutputMode writes m as "WxH@Hz" with exact mHz, which
// ParseOutputMode reads back unchanged. A zero refresh is written as WxH.
func FormatOutputMode(m OutputMode) string {
	if m.RefreshMilli <= 0 {
		return fmt.Sprintf("%dx%d", m.Width, m.Height)
	}
	return fmt.Sprintf("%dx%d@%d.%03d", m.Width, m.Height, m.RefreshMilli/1000, m.RefreshMilli%1000)
}
