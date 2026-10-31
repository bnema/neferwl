package app

import (
	"github.com/bnema/neferwl/internal/adapters/xcursor"
	"github.com/bnema/neferwl/internal/domain"
	"os"
)

// xwaylandSettings is what the built-in XWM publishes to X11 clients: the
// XSETTINGS cursor theme and size and the root cursor image, all from the
// XCURSOR_* environment. The DPI follows the layout inside the XWM.
func xwaylandSettings(cfg domain.XwaylandConfig) domain.XwaylandSettings {
	if cfg.Backend != domain.XwaylandBuiltin {
		return domain.XwaylandSettings{}
	}
	size, theme := xcursor.CursorSize(), os.Getenv("XCURSOR_THEME")
	st := domain.XwaylandSettings{XSettings: cfg.XSettings, CursorTheme: theme, CursorSize: size, RootCursor: true}
	img, err := xcursor.Load(xcursor.SearchPath(), theme, []string{"default", "left_ptr"}, size)
	if err != nil {
		return st // the XWM falls back to the X cursor font
	}
	img = img.ForSize(size, 64)
	st.Cursor = domain.CursorImage{W: img.W, H: img.H, HotX: img.HotX, HotY: img.HotY, Pixels: img.Pixels}
	return st
}
