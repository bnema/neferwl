//go:build race

package wayland

// raceEnabled: the race detector makes purego callbacks (the display loop wake)
// allocate, which allocation budgets that cross the display loop cannot count.
const raceEnabled = true
