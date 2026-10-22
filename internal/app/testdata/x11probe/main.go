// Command x11probe is a minimal X11 client for the Xwayland end-to-end test.
//
// It opens one window filled with solid green, prints "mapped WxH" after the
// first ConfigureNotify and "focus" on each FocusIn, and exits 0 when the
// window manager sends WM_DELETE_WINDOW.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"bnema.dev/xturbo"
	"bnema.dev/xturbo/xproto"
)

const green = 0x00ff00

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "x11probe:", err)
		os.Exit(1)
	}
}

func run() error {
	class := flag.String("class", "X11Probe", "WM_CLASS class (the instance is x11probe)")
	title := flag.String("title", "probe", "_NET_WM_NAME")
	size := flag.String("size", "300x200", "window size WxH")
	sleep := flag.Duration("sleep", 0, "wait before mapping the window")
	flag.Parse()
	w, h, ok := parseSize(*size)
	if !ok {
		return fmt.Errorf("bad -size %q", *size)
	}

	c, err := xturbo.Dial("") // $DISPLAY
	if err != nil {
		return err
	}
	defer c.Close()

	var atoms [4]uint32
	if err := c.InternAtoms([]string{"WM_PROTOCOLS", "WM_DELETE_WINDOW", "_NET_WM_NAME", "UTF8_STRING"}, false, atoms[:]); err != nil {
		return err
	}
	protocols, deleteWin, netWMName, utf8 := xproto.Atom(atoms[0]), xproto.Atom(atoms[1]), xproto.Atom(atoms[2]), xproto.Atom(atoms[3])

	b := c.Buf()
	xwin, err := c.NewXID()
	if err != nil {
		return err
	}
	xgc, err := c.NewXID()
	if err != nil {
		return err
	}
	win, gc := xproto.Window(xwin), xproto.Gcontext(xgc)

	var v xproto.CreateWindowValues
	v.SetEventMask(xproto.EventMaskExposure | xproto.EventMaskStructureNotify | xproto.EventMaskFocusChange)
	xproto.CreateWindow(b, 0, win, xproto.Window(c.Root()), 0, 0, uint16(w), uint16(h), 0, xproto.WindowClassInputOutput, 0, &v)

	wmClass := []byte("x11probe\x00" + *class + "\x00")
	xproto.ChangeProperty(b, xproto.PropModeReplace, win, xproto.AtomWmClass, xproto.AtomString, 8, uint32(len(wmClass)), wmClass)
	name := []byte(*title)
	xproto.ChangeProperty(b, xproto.PropModeReplace, win, netWMName, utf8, 8, uint32(len(name)), name)
	var del [4]byte
	binary.LittleEndian.PutUint32(del[:], uint32(deleteWin))
	xproto.ChangeProperty(b, xproto.PropModeReplace, win, protocols, xproto.AtomAtom, 32, 1, del[:])

	var gv xproto.CreateGCValues
	gv.SetForeground(green)
	xproto.CreateGC(b, gc, xproto.Drawable(win), &gv)
	if err := c.Flush(); err != nil {
		return err
	}
	time.Sleep(*sleep)
	xproto.MapWindow(b, win)

	mapped, done := false, false
	cw, ch := uint16(w), uint16(h) // current size, tracked from ConfigureNotify
	hd := &xturbo.Handler{
		OnEvent: func(ev *xturbo.Event) {
			switch ev.Kind {
			case xproto.KindExpose:
				xproto.PolyFillRectangle(b, xproto.Drawable(win), gc, []xproto.Rectangle{{Width: cw, Height: ch}})
			case xproto.KindConfigureNotify:
				e := xproto.ConfigureNotifyOf(ev)
				if e.Window() != win {
					break
				}
				cw, ch = e.Width(), e.Height()
				if !mapped {
					mapped = true
					fmt.Printf("mapped %dx%d\n", cw, ch)
				}
			case xproto.KindFocusIn:
				fmt.Println("focus")
			case xproto.KindClientMessage:
				e := xproto.ClientMessageOf(ev)
				if e.Type() == protocols && e.Format() == 32 && e.Data().Data32()[0] == uint32(deleteWin) {
					done = true
				}
			}
		},
		OnError: func(e *xturbo.XError) { fmt.Fprintln(os.Stderr, "x11probe:", e) },
	}
	for !done {
		events := int16(unix.POLLIN)
		if err := c.Flush(); err == xturbo.ErrWouldBlock {
			events |= unix.POLLOUT
		} else if err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(c.Fd()), Events: events}}
		if _, err := unix.Poll(fds, -1); err != nil && err != unix.EINTR {
			return err
		}
		if err := c.Dispatch(hd); err != nil {
			return err
		}
	}
	return nil
}

func parseSize(s string) (w, h int, ok bool) {
	ws, hs, found := strings.Cut(s, "x")
	if !found {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(ws)
	h, err2 := strconv.Atoi(hs)
	return w, h, err1 == nil && err2 == nil && w > 0 && h > 0 && w < 0xffff && h < 0xffff
}
