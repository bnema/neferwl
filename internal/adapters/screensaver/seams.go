package screensaver

import (
	"syscall"

	"github.com/bnema/zerobus"
)

// bus is the connection owned by Run. Startup helpers are used before the
// read loop only; afterwards every reply is matched by serial in that loop.
type bus interface {
	RequestName(name string, flags uint32) (uint32, error)
	AddMatch(rule string) error
	SyscallConn() (syscall.RawConn, error)
	Buffered() bool
	ReadMessage() (*zerobus.Message, error)
	NewMethodCall(dest, path, iface, member, sig string) (*zerobus.Encoder, uint32)
	NewReply(m *zerobus.Message, sig string) *zerobus.Encoder
	NewError(m *zerobus.Message, name, sig string) *zerobus.Encoder
	Send() (uint32, error)
	Close() error
}

var _ bus = (*zerobus.Conn)(nil)
