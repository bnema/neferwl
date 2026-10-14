// Package corewake is how an output asks core for the next frame of a running
// animation.
package corewake

import "github.com/bnema/neferwl/internal/ports"

// Send tells core the next frame of a running animation of output is due.
// A frame core has not read yet is enough, so the owner never waits: a full
// channel drops the wake. A nil ch (tests, nobody to wake) does nothing.
// It allocates nothing.
func Send(ch chan<- ports.OutputFrame, output string) {
	if ch == nil {
		return
	}
	select {
	case ch <- ports.OutputFrame{Output: output}:
	default:
	}
}
