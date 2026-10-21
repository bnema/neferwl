package drm

import (
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFocusFlashRequiresComposition(t *testing.T) {
	s, contents := fullscreenScene()
	s.Flash = &ports.FocusFlash{Alpha: 0.5}
	_, reason := scanoutCandidate(s, contents, 200, 100, nil)
	require.Equal(t, "focus_flash", reason)
	s, contents = overlayScene()
	s.Flash = &ports.FocusFlash{Alpha: 0.5}
	_, _, _, reason = overlayCandidate(s, contents, false, nil, nil)
	require.Equal(t, "focus_flash", reason)
}

func TestSelectionOutlineRequiresComposition(t *testing.T) {
	s, contents := fullscreenScene()
	s.Outline = &ports.SelectionOutline{Radius: 12, Width: 2}
	_, reason := scanoutCandidate(s, contents, 200, 100, nil)
	require.Equal(t, "selection_outline", reason)
	s, contents = overlayScene()
	s.Outline = &ports.SelectionOutline{Radius: 12, Width: 2}
	_, _, _, reason = overlayCandidate(s, contents, false, nil, nil)
	require.Equal(t, "selection_outline", reason)
}
