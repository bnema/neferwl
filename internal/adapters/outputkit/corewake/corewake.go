// Package corewake is how an output asks core for the next frame of a running
// animation.
package corewake

import "github.com/bnema/neferwl/internal/domain"

// Send tells core the next frame of a running animation of output is due.
// A frame core has not read yet is enough, so the owner never waits: a full
// channel drops the wake. A nil ch (tests, nobody to wake) does nothing.
// It allocates nothing and reports whether the frame was sent.
func Send(ch chan<- domain.OutputFrame, output string, seq uint64) bool {
	if ch == nil {
		return false
	}
	select {
	case ch <- domain.OutputFrame{Output: output, SceneSeq: seq}:
		return true
	default:
		return false
	}
}
