package ports

import "slices"

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
	// Transform is how the output target holds this scene
	// (wl_output.transform); 0 for capture child scenes. OutputWidth and
	// OutputHeight are already in the transformed (logical) orientation.
	Transform BufferTransform
	// Off turns the display off (output power management): nothing is
	// drawn until a scene without it.
	Off        bool
	Background string
	Border     Border
	// WorkspaceClip bounds workspace windows, their popups and separators
	// in logical output coordinates. Zero inherits the full output. Layers
	// remain output-wide; overview previews do not use this clip.
	WorkspaceClip Rect
	// TileClip bounds tiles (SceneWindow.Tile) and their lines inside
	// WorkspaceClip: the usable area exclusive layers leave, so a tile is
	// never drawn over a panel, whatever its layer. Zero does not clip
	// (overview, capture scenes).
	TileClip Rect
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

// SameAs reports whether o draws exactly what s draws: every field other
// than Seq is equal. A scene carrying a CaptureScene is never the same
// (conservative: the capture image is not compared). Add every new Scene
// field here; TestSceneSameAsCoversEveryField fails until the field count
// is updated.
func (s Scene) SameAs(o Scene) bool {
	if s.Security != o.Security || s.Output != o.Output ||
		s.OutputWidth != o.OutputWidth || s.OutputHeight != o.OutputHeight ||
		s.Scale != o.Scale || s.Transform != o.Transform || s.Off != o.Off ||
		s.Background != o.Background || s.Border != o.Border ||
		s.WorkspaceClip != o.WorkspaceClip || s.TileClip != o.TileClip || s.Dim != o.Dim || s.DimBehind != o.DimBehind {
		return false
	}
	if s.CaptureScene != nil || o.CaptureScene != nil {
		return false
	}
	if (s.Capture == nil) != (o.Capture == nil) {
		return false
	}
	if s.Capture != nil {
		a, b := *s.Capture, *o.Capture
		if a.Shown != b.Shown || a.Session != b.Session || a.Revision != b.Revision ||
			a.Workspace != b.Workspace || a.Window != b.Window || !slices.Equal(a.Excluded, b.Excluded) {
			return false
		}
	}
	return slices.Equal(s.Windows, o.Windows) && slices.Equal(s.Separators, o.Separators) &&
		slices.Equal(s.DropHints, o.DropHints) && slices.Equal(s.Layers, o.Layers) &&
		slices.Equal(s.CaptureIndicators, o.CaptureIndicators)
}

// Shows reports whether the scene draws the surface of id: only its
// content changes need a new frame. A window scrolled off the output is
// not drawn. WorkspaceClip also excludes off-viewport windows, TileClip
// tiles under a panel; OverLayers popups remain output-wide.
func (s Scene) Shows(id WindowID) bool {
	for _, w := range s.Windows {
		if w.ID == id && !w.Hidden && w.Rect.Overlaps(Rect{W: s.OutputWidth, H: s.OutputHeight}) &&
			(w.OverLayers || s.WorkspaceClip == (Rect{}) || w.Rect.Overlaps(s.WorkspaceClip)) &&
			(!w.Tile() || s.TileClip == (Rect{}) || w.Rect.Overlaps(s.TileClip)) {
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
	// Fade makes the whole window translucent, border and content: 0 is
	// opaque, 1 invisible (like SurfaceContent.Fade). A window appearing
	// or leaving animates it; a faded window is composed, never scanned
	// out.
	Fade float64
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
	// Rect (an overview thumbnail): the client keeps its size. A preview
	// is a card: it never opens the floats.
	Preview float64
	// Zoom, above 0, is the content scale of a window whose frame is
	// animating (appearing, leaving, an overview card in flight): it
	// replaces Preview for drawing only; the window keeps its kind.
	Zoom float64
	// Popups are drawn from their content only: no border, no background.
	Popup bool
	// OverLayers popups hang from a layer surface: drawn over the top and
	// overlay layers. Window popups stay with the windows, under them.
	OverLayers bool
}

// Tile reports whether w is a tile: laid out in the usable area and
// clipped to Scene.TileClip. Floats, fullscreen windows, overview previews
// and popups are placed against the whole viewport.
func (w SceneWindow) Tile() bool {
	return !w.Floating && !w.Fullscreen && !w.Popup && w.Preview == 0
}
