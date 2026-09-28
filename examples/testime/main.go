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
)

func main() {
	preedit := flag.String("preedit", "nihon", "preedit string sent first")
	commit := flag.String("commit", "日本", "text committed after the preedit")
	delay := flag.Duration("delay", 100*time.Millisecond, "time between preedit and commit")
	once := flag.Bool("once", false, "exit after the first commit")
	flag.Parse()
	if err := run(*preedit, *commit, *delay, *once); err != nil {
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
	pending    bool // a text input to serve after done
}

func (m *inputMethod) Dispatch(e *wlturbo.Event) {
	switch uint32(e.Opcode) {
	case inputmethod.ZwpInputMethodV2EventActivate:
		m.activating = true
	case inputmethod.ZwpInputMethodV2EventDeactivate:
		m.activating = false
	case inputmethod.ZwpInputMethodV2EventDone:
		m.dones++
		if m.activating && !m.active {
			m.pending = true
		}
		m.active = m.activating
	case inputmethod.ZwpInputMethodV2EventUnavailable:
		m.unavail = true
	}
}

// ignored drops the events of an object testime does not use.
type ignored struct{ wlturbo.BaseProxy }

func (*ignored) Dispatch(*wlturbo.Event) {}

func (m *inputMethod) send(op uint32, args ...any) error {
	return m.c.SendRequest(m.ID(), uint16(op), args...)
}

func run(preedit, commit string, delay time.Duration, once bool) error {
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
	c.Context().Register(seatProxy)
	manager, err := bind("zwp_input_method_manager_v2")
	if err != nil {
		return err
	}
	m := &inputMethod{c: c}
	m.SetID(c.AllocateID())
	c.Context().Register(m)
	if err := c.SendRequest(manager, uint16(inputmethod.ZwpInputMethodManagerV2RequestGetInputMethod), seat, m.ID()); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "testime: ready")
	for {
		if err := c.Dispatch(); err != nil {
			return err
		}
		if m.unavail {
			return errors.New("another input method is running")
		}
		if !m.pending {
			continue
		}
		m.pending = false
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
		if once {
			return nil
		}
	}
}
