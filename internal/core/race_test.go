//go:build race

package core

// raceEnabled: the race runtime adds allocations, so allocation budgets
// keep a separate ceiling for it.
const raceEnabled = true
