package ports

import "os"

// Scene carries core → renderer immutable snapshots with fresh Windows slices,
// one per output. Rects and the output size are logical and local to the
// output; the renderer multiplies by Scale.
type Scene struct {
	// Security is the owner epoch captured when this scene was published.
	// Output owners reject stale scenes rather than showing a queued desktop
	// or accepting old locker buffers across acquisition/release.
	Security                  SecurityState
	Output                    string
	Seq                       uint64
	OutputWidth, OutputHeight int
	Scale                     float64
	// Off turns the display off (output power management): nothing is
	// drawn until a scene without it.
	Off        bool
	Background string
	Border     Border
	// WorkspaceClip bounds workspace windows, their popups and separators
	// in logical output coordinates. Zero inherits the full output. Layers
	// remain output-wide; overview previews do not use this clip.
	WorkspaceClip Rect
	// Dim darkens the background, bottom layers and tiles (tile lines
	// included) inside WorkspaceClip when set, with black at this opacity,
	// 0 to 1, under the first
	// visible float. 0 draws nothing.
	Dim float64
	// DimBehind draws the Dim veil under every window instead, right over
	// the background and bottom layers (the overview).
	DimBehind bool
	Windows   []SceneWindow
	// Separators are the lines between windows, drawn in slice order with
	// the Border colors: tile lines over the tiles, under the floats; a
	// float's border right after the float.
	Separators []Separator
	// DropHints show where a dragged tile lands, filled with Border.Active
	// over the windows of the workspace, under the top layers. Only the
	// output under the pointer has them, during a drag.
	DropHints []Rect
	// Layers are the shown layer surfaces, drawn in slice order: background
	// and bottom before windows, top and overlay after. Core leaves out
	// those a fullscreen window hides (all but the background and a surface
	// taking the keyboard exclusively); adapters draw every layer given.
	// Window popups are drawn after the windows, layer popups
	// (SceneWindow.OverLayers) last, over every layer.
	Layers []SceneLayer
	// Capture is the capture state of this output: exclusion and hidden
	// workspace; nil while nothing is captured (capture.go).
	Capture *SceneCapture
	// CaptureScene is the workspace captured off screen (Capture.Workspace)
	// drawn for capture only: a workspace that is not on screen. Only the
	// root scene carries it; CaptureScene.Capture and .CaptureScene are nil.
	CaptureScene *Scene
	// CaptureIndicators are the marks of live captures of this output,
	// drawn over everything; nil when none. Captures never hold them: the
	// capture pipeline serves requests from a scene without them.
	CaptureIndicators []CaptureIndicator
}

// Shows reports whether the scene draws the surface of id: only its
// content changes need a new frame. A window scrolled off the output is
// not drawn. WorkspaceClip also excludes off-viewport windows; OverLayers
// popups remain output-wide.
func (s Scene) Shows(id WindowID) bool {
	for _, w := range s.Windows {
		if w.ID == id && !w.Hidden && w.Rect.Overlaps(Rect{W: s.OutputWidth, H: s.OutputHeight}) &&
			(w.OverLayers || s.WorkspaceClip == (Rect{}) || w.Rect.Overlaps(s.WorkspaceClip)) {
			return true
		}
	}
	for _, l := range s.Layers {
		if l.ID == id {
			return true
		}
	}
	return false
}

// Border carries the window border style; colors are #rrggbb, "" skips
// drawing. Width is in logical pixels.
type Border struct {
	Width            int
	Active, Inactive string
}

// Separator is a line between windows, in logical pixels. Active lines
// mark the focused window and use Border.Active, others Border.Inactive.
// Window is the float whose border it is; 0 for lines between tiles.
type Separator struct {
	Rect   Rect
	Active bool
	Window WindowID
}

// SceneLayer carries core → renderer layer surface placement.
type SceneLayer struct {
	ID    WindowID
	Layer Layer
	Rect  Rect
}

// SurfaceColor is the committed encoding of a surface. Zero means sRGB.
// PQ+BT.2020 and extended-linear sRGB are color-managed; zero is legacy
// sRGB. MaxCLL/MaxFALL are in nits. YUV metadata is per surface commit.
type SurfaceColor struct {
	TF, Primaries   uint8
	MaxCLL, MaxFALL uint16
	// Coefficients/range follow wp_color_representation_v1 (zero =
	// unspecified, for YUV interpreted as BT.709 limited). Chroma is a
	// H.273 Chroma420SampleLocType + 1 (zero defaults to type 0).
	Coefficients, Range, Chroma uint8
}

const (
	ColorTFPQ             uint8 = 11 // wp_color_manager_v1 ST2084 PQ
	ColorTFExtendedLinear uint8 = 5  // wp_color_manager_v1 ext_linear
	ColorPrimariesBT2020  uint8 = 6  // wp_color_manager_v1 BT.2020
	ColorPrimariesSRGB    uint8 = 1
)

func (c SurfaceColor) IsPQ2020() bool {
	return c.TF == ColorTFPQ && c.Primaries == ColorPrimariesBT2020
}

func (c SurfaceColor) IsExtendedLinear() bool {
	return c.TF == ColorTFExtendedLinear && c.Primaries == ColorPrimariesSRGB
}

// ContentType is the committed wp_content_type_v1 hint.
type ContentType uint8

const (
	ContentNone ContentType = iota
	ContentPhoto
	ContentVideo
	ContentGame
)

// SurfaceContent carries wayland → output the latest committed content of a
// window: SHM, a client shared-memory buffer, or DMABuf, a GPU buffer.
// Renderers read both in place and never modify them. Empty means the window
// has no content. LogicalW and LogicalH are the surface size in logical
// pixels (buffer scale and viewport applied). Subsurfaces come in Children;
// Geometry is the part of the surface that is the window (xdg window
// geometry), the rest being client shadows.
type SurfaceContent struct {
	ID          WindowID
	ContentType ContentType
	// Surface is a never-reused wl_surface identity; Version identifies its
	// applied buffer content independently of the window publication Seq.
	Surface, Version uint64
	// Seq counts the window's contents (wayland sets it), for release.
	Seq                uint64
	Width, Height      int
	LogicalW, LogicalH int
	// Source is the committed crop in buffer pixels: x, y, width, height.
	// Zero width means the entire buffer is used.
	Source [4]float32
	// Transform is the wl_output.transform the client applied to the
	// buffer: readers apply its inverse. Width and Height stay the buffer's.
	Transform BufferTransform
	// Opaque ignores the alpha byte: an x format, or an opaque region that
	// covers the whole surface. It is false while Fade is set.
	Opaque bool
	// Fade is how much wp_alpha_modifier_v1 fades the surface: 0 none,
	// 1 invisible. Its opacity is multiplied by 1-Fade.
	Fade   float32
	Color  SurfaceColor
	SHM    *SHMBuffer
	DMABuf *DMABuf
	// Children are the subsurfaces, bottom to top, flattened.
	Children []Subsurface
	// Geometry is in logical pixels from the surface origin; empty means
	// the whole surface.
	Geometry Rect
	// Async asks for tearing presentation (wp_tearing_control_v1); outputs
	// honour it only in direct scanout.
	Async bool
	// Acquire is the explicit-sync fence of this content's root buffer
	// (wp_linux_drm_syncobj_v1): readers wait on it before reading the
	// buffer, instead of the buffer's implicit fences. Wayland owns it and
	// closes it when the buffer is released; a reader that keeps it past
	// the call duplicates it under Acquire.SyscallConn. nil: implicit sync.
	// Wayland closes it only after every output reported a later content of
	// the window as seen, so a reader never gets it already closed.
	Acquire *os.File
	// DamageHistory is what changed in the root surface's buffer over the
	// window's last contents, oldest first, ending with this one (Seq).
	// A renderer holding an older content redraws the union of the
	// entries after it, or everything when the history does not reach it.
	DamageHistory []SeqDamage
}

// BufferTransform is a wl_output.transform value: bit 0 rotates 90°
// counter-clockwise, bit 1 rotates 180°, bit 2 flips around the vertical
// axis first.
type BufferTransform uint8

// Rotated reports whether width and height swap between buffer and surface.
func (t BufferTransform) Rotated() bool { return t&1 != 0 }

// ToBuffer maps a point of a w×h surface (logical, before scale) to the
// same point of its buffer, in the buffer's untransformed w'×h' axes, where
// w' and h' swap when the transform is Rotated.
func (t BufferTransform) ToBuffer(x, y, w, h float64) (float64, float64) {
	if t&4 != 0 {
		x = w - x
	}
	switch t & 3 {
	case 1:
		return y, w - x
	case 2:
		return w - x, h - y
	case 3:
		return h - y, x
	}
	return x, y
}

// SeqDamage is what content Seq changed from the one before: Rects in
// root buffer pixels, or everything when Full.
type SeqDamage struct {
	Seq   uint64
	Full  bool
	Rects []Rect
}

// DamageSince is the union of changes after content seq up to this one,
// and false when the history does not reach back to seq (redraw all).
func (c SurfaceContent) DamageSince(seq uint64) ([]Rect, bool) {
	if seq >= c.Seq {
		return nil, true
	}
	h := c.DamageHistory
	if len(h) == 0 || h[0].Seq > seq+1 || h[len(h)-1].Seq != c.Seq {
		return nil, false
	}
	var out []Rect
	for _, d := range h {
		if d.Seq <= seq {
			continue
		}
		if d.Full {
			return nil, false
		}
		out = append(out, d.Rects...)
	}
	return out, true
}

// Subsurface is a child surface at X, Y logical pixels from the root
// surface origin. Below children are drawn under the root surface.
type Subsurface struct {
	X, Y  int
	Below bool
	SurfaceContent
}

// Empty reports whether the content has nothing to draw.
func (c SurfaceContent) Empty() bool {
	return c.SHM == nil && c.DMABuf == nil && len(c.Children) == 0
}

// SHMBuffer is a client wl_shm buffer: B8G8R8A8 pixels (argb8888/xrgb8888
// little-endian) at Offset in the pool File, Stride bytes per row. The
// client keeps writing the file, so renderers copy from it when they draw.
// Pool is unique per wl_shm pool for the session: renderers map a pool
// once. File stays open while the client's buffer exists and follows the
// DMABuf rules: duplicate it under File.SyscallConn before keeping it. A
// client may shrink the file at any time, so readers must survive faults.
type SHMBuffer struct {
	Pool           uint64
	File           *os.File
	Offset, Stride int
}

// DMABuf is a client GPU buffer (linux-dmabuf). Its files stay open while
// the client's buffer exists; a renderer that keeps it must duplicate the
// descriptors under File.SyscallConn so a concurrent close cannot hand it a
// reused one. ID is unique per buffer for the whole session: renderers key
// their imports on it.
type DMABuf struct {
	ID            uint64
	Width, Height int
	Format        uint32 // DRM fourcc
	Modifier      uint64
	Planes        []DMABufPlane
}

// DMABufPlane is one plane of a DMABuf.
type DMABufPlane struct {
	File           *os.File
	Offset, Stride uint32
}

// DMABufFormat is a DRM fourcc with a modifier the renderer can import.
type DMABufFormat struct {
	Format   uint32
	Modifier uint64
}

// DMABufSupport is what the renderer imports: the render device (a dev_t,
// 0 when unknown) and the formats. No formats means no dmabuf.
type DMABufSupport struct {
	Device  uint64
	Formats []DMABufFormat
}

// SceneWindow carries core → renderer window placement.
type SceneWindow struct {
	ID                          WindowID
	Rect                        Rect
	Focused, Fullscreen, Hidden bool
	// Inset sides carry a separator line inside Rect: the client is drawn
	// inside it.
	Inset Sides
	// Floating windows draw at their placed size. Below marks a native
	// covering float behind the column group.
	Floating, Below bool
	// Dim darkens the window, border included, with black at this
	// opacity, 0 to 1: a stashed window peeking in. 0 draws nothing.
	Dim float64
	// FocusEffect, above 0, is the focus indicator's effect at this frame
	// (focus.animation, focus.effect): the window's surfaces are lifted
	// toward white by that fraction (screen blend). Renderers without the
	// effect ignore it.
	//
	// Only focus.effect = screen exists, so the effect kind is not carried:
	// core ignores Config.Focus.Effect and the shaders always apply screen
	// (compose.frag, compose_hdr.frag, push constant mapy.z). A second effect
	// needs its kind here and in the push constants (a misc flag).
	FocusEffect float64
	// Preview, above 0, draws the window's surfaces that much smaller in
	// Rect (an overview thumbnail): the client keeps its size.
	Preview float64
	// Popups are drawn from their content only: no border, no background.
	Popup bool
	// OverLayers popups hang from a layer surface: drawn over the top and
	// overlay layers. Window popups stay with the windows, under them.
	OverLayers bool
}
