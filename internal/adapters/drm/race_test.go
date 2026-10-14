//go:build race

package drm

// raceEnabled: the race runtime changes allocation counts, which budgets
// measured against a mocked call cannot tell apart from a regression.
const raceEnabled = true
