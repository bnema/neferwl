package atspi

import (
	"github.com/bnema/neferwl/internal/domain"
)

// AT-SPI constants (atspi-constants.h). The role ids are the AtspiRole enum
// values; the state ids are bit indices of the state set.
const (
	roleCheckBox      = 7
	roleComboBox      = 11
	roleDialog        = 16
	roleFrame         = 23
	roleMenuItem      = 35
	rolePageTab       = 37
	rolePushButton    = 43
	roleRadioButton   = 44
	roleToggleButton  = 62
	roleWindow        = 69
	roleLink          = 88
	roleDocumentFrame = 82
	roleDocumentWeb   = 95

	stateActive  = 1
	stateShowing = 25
	stateVisible = 30

	roleWords  = 5 // int32 words of a role set in a MatchRule
	stateWords = 2 // int32 words of a state set in a MatchRule

	// AtspiCollectionMatchType and AtspiCollectionSortOrder values.
	matchAll       = 1
	matchAny       = 2
	sortCanonical  = 1
	matchCount     = 500
	maxDepth       = 30
	maxWalkNodes   = 3000
	maxFrames      = 32
	sizeTolerance  = 40
	maxMatchesRead = matchCount
)

// clickableRoles are the roles that become hint targets.
var clickableRoles = []uint32{
	roleLink, rolePushButton, roleToggleButton, roleCheckBox,
	roleRadioButton, roleMenuItem, rolePageTab, roleComboBox,
}

// matchRule is the Collection query: showing and visible objects of the
// clickable roles (only links when linksOnly), plus web documents for coverage.
type matchRule struct{ linksOnly bool }

func (r matchRule) roles() []uint32 {
	if r.linksOnly {
		return []uint32{roleLink, roleDocumentFrame, roleDocumentWeb}
	}
	return append(append([]uint32(nil), clickableRoles...), roleDocumentFrame, roleDocumentWeb)
}

// roleBits packs role ids into the bit set of a MatchRule: bit id%32 of word
// id/32.
func roleBits(ids []uint32) []int32 {
	words := make([]int32, roleWords)
	for _, id := range ids {
		if w := int(id / 32); w < len(words) {
			words[w] |= int32(uint32(1) << (id % 32))
		}
	}
	return words
}

// stateBits packs state ids into the bit set of a MatchRule.
func stateBits(ids ...uint32) []int32 {
	words := make([]int32, stateWords)
	for _, id := range ids {
		if w := int(id / 32); w < len(words) {
			words[w] |= int32(uint32(1) << (id % 32))
		}
	}
	return words
}

func hasState(set uint64, state uint) bool { return set&(1<<state) != 0 }

// isClickable reports whether role makes a target.
func isClickable(role uint32, linksOnly bool) bool {
	if linksOnly {
		return role == roleLink
	}
	for _, r := range clickableRoles {
		if r == role {
			return true
		}
	}
	return false
}

// isWindowRole reports whether role is a top-level window role.
func isWindowRole(role uint32) bool {
	return role == roleFrame || role == roleWindow || role == roleDialog
}

// kindOf maps a role to the hint kind: links are links, the rest buttons.
func kindOf(role uint32) domain.HintKind {
	if role == roleLink {
		return domain.HintLink
	}
	return domain.HintButton
}

// frameInfo is a showing top-level window of an app.
type frameInfo struct {
	ref    ref
	active bool
	w, h   int // size of the frame, from its window extents
}

// matchFrames pairs the windows of one app with its frames, one to one. A
// window is paired with a frame when the pairing is certain:
//   - one window and one frame;
//   - the focused window with the one ACTIVE frame; that frame is then
//     reserved and no other window can take it;
//   - otherwise the one unreserved frame whose size is within sizeTolerance
//     of the window, provided no other unmatched window claims it as well.
//
// Ambiguous windows are left out. The result maps the index of a window in
// wins to the index of its frame in frames.
func matchFrames(wins []domain.HintWindow, frames []frameInfo) map[int]int {
	out := map[int]int{}
	if len(wins) == 1 && len(frames) == 1 {
		out[0] = 0
		return out
	}
	// Active matches first: they are reserved against the size matches.
	active, n := -1, 0
	for j, f := range frames {
		if f.active {
			active, n = j, n+1
		}
	}
	reserved := make([]bool, len(frames))
	done := make([]bool, len(wins))
	if n == 1 {
		focused := -1
		count := 0
		for i, w := range wins {
			if w.Focused {
				focused, count = i, count+1
			}
		}
		if count == 1 {
			out[focused] = active
			reserved[active] = true
			done[focused] = true
		}
	}
	claims := make([][]int, len(wins)) // size candidates per window
	owners := make([]int, len(frames)) // windows that size-match a frame
	for i, w := range wins {
		if done[i] {
			continue
		}
		for j, f := range frames {
			if !reserved[j] && absInt(f.w-w.Rect.W) <= sizeTolerance && absInt(f.h-w.Rect.H) <= sizeTolerance {
				claims[i] = append(claims[i], j)
				owners[j]++
			}
		}
	}
	for i := range wins {
		if !done[i] && len(claims[i]) == 1 && owners[claims[i][0]] == 1 {
			out[i] = claims[i][0]
		}
	}
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// element is a match with its extents.
type element struct {
	role       uint32
	x, y, w, h int32 // window-relative
}

// toTarget converts an element of a window into a logical output-local
// target: the element extents are relative to the top-level frame, whose own
// window-relative origin is (fx, fy); the result is clipped to the window.
// It reports false for an empty result.
func toTarget(win domain.HintWindow, fx, fy int32, e element) (domain.HintTarget, bool) {
	if e.w <= 0 || e.h <= 0 {
		return domain.HintTarget{}, false
	}
	r := domain.Rect{X: win.Rect.X + int(e.x-fx), Y: win.Rect.Y + int(e.y-fy), W: int(e.w), H: int(e.h)}
	// A target crossing disjoint rectangles cannot be represented by one Rect.
	// Keep the largest intersection (stable region order breaks ties).
	var clipped domain.Rect
	for i := -1; i < len(win.Popups); i++ {
		area := win.Rect
		if i >= 0 {
			area = win.Popups[i]
		}
		x0, y0 := max(r.X, area.X), max(r.Y, area.Y)
		x1, y1 := min(r.X+r.W, area.X+area.W), min(r.Y+r.H, area.Y+area.H)
		if x1 > x0 && y1 > y0 && (x1-x0)*(y1-y0) > clipped.W*clipped.H {
			clipped = domain.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
		}
	}
	if clipped.W == 0 {
		return domain.HintTarget{}, false
	}
	return domain.HintTarget{Rect: clipped, Kind: kindOf(e.role), Source: domain.HintSourceATSPI, Score: 1, Window: win.ID}, true
}

func isDocument(role uint32) bool { return role == roleDocumentFrame || role == roleDocumentWeb }
