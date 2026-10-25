package powerkey

import "github.com/bnema/zerobus"

type bus interface {
	UnixFDs() bool
	AddMatch(string) error
	NewMethodCall(dest, path, iface, member, sig string) (*zerobus.Encoder, uint32)
	Send() (uint32, error)
	ReadMessage() (*zerobus.Message, error)
	Close() error
}

var _ bus = (*zerobus.Conn)(nil)
