package screencast

import "os"

// callerState is how far the proof of a D-Bus peer's executable got.
type callerState uint8

const (
	callerFree callerState = iota
	// callerPid: waiting for the bus to name the peer's pid.
	callerPid
	// callerConfirm: a pidfd was opened; waiting for the bus to name the same
	// pid again, which proves the pidfd names the peer.
	callerConfirm
	// callerKnown: exe is the executable of the peer.
	callerKnown
)

// caller is one checked unique bus name. The pidfd stays open for as long
// as the name is cached, so the executable can be read again at the
// privileged entry points.
type caller struct {
	state  callerState
	name   string // unique name, ":1.42"
	serial uint32 // the outstanding GetConnectionUnixProcessID request
	pid    int
	pidfd  *os.File
	exe    string
}

func (c *caller) release() {
	if c.pidfd != nil {
		_ = c.pidfd.Close()
	}
	*c = caller{}
}

// pending is a request parked until its sender's check ends.
type pending struct {
	used bool
	req  request
}
