// Command testime is a minimal zwp_input_method_v2 client for testing text
// input. Each time a text input is activated it sends a preedit string,
// then commits the final text:
//
//	WAYLAND_DISPLAY=wayland-1 go run ./examples/testime -preedit nihon -commit 日本
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bnema/purego-libwayland/protocol/inputmethod"
	"github.com/bnema/wlturbo"
	clientcore "github.com/bnema/wlturbo/protocol/core"
	clientime "github.com/bnema/wlturbo/protocol/inputmethod"
)

func main() {
	preedit := flag.String("preedit", "nihon", "preedit string sent first")
	commit := flag.String("commit", "日本", "text committed after the preedit")
	delay := flag.Duration("delay", 100*time.Millisecond, "time between preedit and commit")
	count := flag.Int("count", 0, "exit after this many commits; 0 runs until the compositor closes")
	flag.Parse()
	if err := run(*preedit, *commit, *delay, *count); err != nil {
		fmt.Fprintln(os.Stderr, "testime:", err)
		os.Exit(1)
	}
}

// inputMethod follows the activate/done cycle of zwp_input_method_v2.
type inputMethod struct {
	wlturbo.BaseProxy
	c          *wlturbo.Display
	dones      uint32
	active     bool // state of the last done
	activating bool // activate seen, not yet applied by done
	unavail    bool
	activated  bool // activate seen since the last done
	serve      bool // an activation applied by done, not yet served
}

// EventSignature delegates to the generated client schema, including events
// this example intentionally ignores and their descriptor ownership.
func (*inputMethod) EventSignature(op uint16) (string, bool) {
	return (&clientime.InputMethod{}).EventSignature(op)
}

func (m *inputMethod) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case inputmethod.ZwpInputMethodV2EventActivate:
		// Every activate starts a new text input session, even while
		// active: an app enabling its text input again gets one.
		m.activating, m.activated = true, true
	case inputmethod.ZwpInputMethodV2EventDeactivate:
		m.activating, m.activated = false, false
	case inputmethod.ZwpInputMethodV2EventDone:
		// Requests only count for an activation once its done is in:
		// the compositor drops commits with an older serial.
		m.dones++
		m.active = m.activating
		m.serve = m.serve && m.active || m.activated
		m.activated = false
	case inputmethod.ZwpInputMethodV2EventUnavailable:
		m.unavail = true
	}
}

// ignored drops the events of an object testime does not use.
type ignored struct{ wlturbo.BaseProxy }

func (*ignored) Dispatch(*wlturbo.Event) {}

func (*ignored) EventSignature(op uint16) (string, bool) {
	return (&clientcore.Seat{}).EventSignature(op)
}

func (m *inputMethod) send(op uint32, args ...any) error {
	return m.c.SendRequest(m.ID(), uint16(op), args...)
}

func run(preedit, commit string, delay time.Duration, count int) error {
	c, err := wlturbo.Connect("")
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Roundtrip(); err != nil {
		return err
	}
	bind := func(iface string) (uint32, error) {
		g, ok := c.Registry().FindGlobal(iface)
		if !ok {
			return 0, fmt.Errorf("compositor has no %s", iface)
		}
		return c.Registry().BindID(g.Name, g.Interface, 1)
	}
	seat, err := bind("wl_seat")
	if err != nil {
		return err
	}
	// wlturbo fails on events for unregistered objects: the seat sends some.
	seatProxy := &ignored{}
	seatProxy.SetID(seat)
	seatProxy.SetContext(c.Context())
	seatProxy.SetVersion(1)
	c.Context().Register(seatProxy)
	manager, err := bind("zwp_input_method_manager_v2")
	if err != nil {
		return err
	}
	m := &inputMethod{c: c}
	m.SetID(c.AllocateID())
	m.SetContext(c.Context())
	m.SetVersion(1)
	c.Context().Register(m)
	if err := c.SendRequest(manager, uint16(inputmethod.ZwpInputMethodManagerV2RequestGetInputMethod), seat, m.ID()); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "testime: ready")
	for commits := 0; ; {
		if err := c.Dispatch(); err != nil {
			return err
		}
		if m.unavail {
			return errors.New("another input method is running")
		}
		if !m.serve {
			continue
		}
		m.serve = false
		if err := m.send(inputmethod.ZwpInputMethodV2RequestSetPreeditString, preedit, int32(len(preedit)), int32(len(preedit))); err != nil {
			return err
		}
		if err := m.send(inputmethod.ZwpInputMethodV2RequestCommit, m.dones); err != nil {
			return err
		}
		time.Sleep(delay)
		// Events that arrived meanwhile update the done count first.
		if err := c.Roundtrip(); err != nil {
			return err
		}
		if !m.active {
			continue
		}
		if err := m.send(inputmethod.ZwpInputMethodV2RequestCommitString, commit); err != nil {
			return err
		}
		if err := m.send(inputmethod.ZwpInputMethodV2RequestCommit, m.dones); err != nil {
			return err
		}
		if err := c.Roundtrip(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "testime: committed %q\n", commit)
		if commits++; count > 0 && commits == count {
			return nil
		}
	}
}
