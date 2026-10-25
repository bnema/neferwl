package screensaver

// These are real wire clients of the private dbus-daemon, not test doubles.
import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/zerobus"
)

type testClient struct{ *zerobus.Conn }
type clientObject struct {
	c          *testClient
	dest, path string
}
type clientReply struct {
	m   *zerobus.Message
	Err error
}

func dialClient(address string) (*testClient, error) {
	c, err := zerobus.Dial(address)
	if err != nil {
		return nil, err
	}
	return &testClient{c}, nil
}
func (c *testClient) Names() []string                       { return []string{c.UniqueName()} }
func (c *testClient) Object(dest, path string) clientObject { return clientObject{c, dest, path} }
func (c *testClient) BusObject() clientObject               { return c.Object(busDaemon, "/org/freedesktop/DBus") }
func (c *testClient) ReleaseName(name string) (uint32, error) {
	var result uint32
	err := c.BusObject().Call(busDaemon+".ReleaseName", 0, name).Store(&result)
	return result, err
}
func (o clientObject) encode(method string, flags zerobus.Flags, args ...any) {
	split := strings.LastIndexByte(method, '.')
	sig := ""
	for _, arg := range args {
		switch arg.(type) {
		case string:
			sig += "s"
		case uint32:
			sig += "u"
		case bool:
			sig += "b"
		default:
			panic("unsupported wire argument")
		}
	}
	e := o.c.NewCall(o.dest, o.path, method[:split], method[split+1:], sig)
	e.SetFlags(flags)
	for _, arg := range args {
		switch v := arg.(type) {
		case string:
			e.Str(v)
		case uint32:
			e.Uint32(v)
		case bool:
			e.Bool(v)
		}
	}
}
func (o clientObject) Call(method string, flags zerobus.Flags, args ...any) *clientReply {
	o.encode(method, flags, args...)
	m, err := o.c.Call()
	return &clientReply{m, err}
}
func (o clientObject) CallWithContext(ctx context.Context, method string, flags zerobus.Flags, args ...any) *clientReply {
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			_ = o.c.Close()
		case <-done:
		}
	}()
	r := o.Call(method, flags, args...)
	close(done)
	<-joined
	return r
}
func (o clientObject) Go(method string, flags zerobus.Flags, _ any, args ...any) {
	o.encode(method, flags, args...)
	_, _ = o.c.Send()
}
func (r *clientReply) Store(out any) error {
	if r.Err != nil {
		return r.Err
	}
	reader := r.m.Body()
	switch v := out.(type) {
	case *string:
		if r.m.Signature != "s" {
			return fmt.Errorf("expected string reply")
		}
		*v = reader.Str()
	case *uint32:
		if r.m.Signature != "u" {
			return fmt.Errorf("expected uint32 reply")
		}
		*v = reader.Uint32()
	case *bool:
		if r.m.Signature != "b" {
			return fmt.Errorf("expected bool reply")
		}
		*v = reader.Bool()
	default:
		return fmt.Errorf("unsupported reply destination")
	}
	return reader.Err()
}
