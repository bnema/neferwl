package drm

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestFocusFlashRequiresComposition(t *testing.T) {
	s, contents := fullscreenScene()
	s.Flash = &domain.FocusFlash{Alpha: 0.5}
	_, reason := scanoutCandidate(s, contents, 200, 100, nil)
	require.Equal(t, "focus_flash", reason)
	s, contents = overlayScene()
	s.Flash = &domain.FocusFlash{Alpha: 0.5}
	_, _, _, reason = overlayCandidate(s, contents, false, nil, nil)
	require.Equal(t, "focus_flash", reason)
}

func TestSelectionOutlineRequiresComposition(t *testing.T) {
	s, contents := fullscreenScene()
	s.Outline = &domain.SelectionOutline{Radius: 12, Width: 2}
	_, reason := scanoutCandidate(s, contents, 200, 100, nil)
	require.Equal(t, "selection_outline", reason)
	s, contents = overlayScene()
	s.Outline = &domain.SelectionOutline{Radius: 12, Width: 2}
	_, _, _, reason = overlayCandidate(s, contents, false, nil, nil)
	require.Equal(t, "selection_outline", reason)
}
