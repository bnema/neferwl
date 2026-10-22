// Command x11probe is a minimal X11 client for the Xwayland end-to-end test.
//
// It opens one window filled with solid green, prints "mapped WxH" after the
// first ConfigureNotify and "focus" on each FocusIn, and exits 0 when the
// window manager sends WM_DELETE_WINDOW.
//
// With -override it also maps a magenta override-redirect window after the
// main one is mapped (prints "override mapped") and unmaps it after
// -override-for (prints "override unmapped"). With -override-dismiss-on-focusout
// it unmaps it instead when the main window loses focus (prints "dismissed").
//
// With -fullscreen it asks the window manager for _NET_WM_STATE_FULLSCREEN
// -fullscreen-delay after the window is mapped (0: right at the map) and
// prints "fullscreen WxH" when the window is resized; -fullscreen-off asks to
// leave it that long after (prints "fullscreen off WxH"). -keyboard-grab
// calls XGrabKeyboard on the window once it is mapped (prints "grab").
// -at X,Y creates the window at that root position.
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
	"bnema.dev/xturbo/shape"
	"bnema.dev/xturbo/xproto"
)

const (
	green   = 0x00ff00
	magenta = 0xff00ff
)

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
	override := flag.Bool("override", false, "map a magenta override-redirect window after the main one")
	overrideAt := flag.String("override-at", "100,50", "override window position X,Y in root coordinates")
	overrideSize := flag.String("override-size", "120x80", "override window size WxH")
	overrideFor := flag.Duration("override-for", 0, "unmap the override window after this delay (0: never)")
	overrideType := flag.String("override-type", "menu", "override window type: menu or tooltip")
	overrideShape := flag.Bool("override-shape", false, "give the override window an empty input shape")
	dismiss := flag.Bool("override-dismiss-on-focusout", false, "unmap the override window when the main window loses focus")
	fullscreen := flag.Bool("fullscreen", false, "request _NET_WM_STATE_FULLSCREEN after the window is mapped")
	fullscreenDelay := flag.Duration("fullscreen-delay", 0, "delay between the map and the fullscreen request")
	fullscreenOff := flag.Duration("fullscreen-off", 0, "leave fullscreen this long after the request (0: never)")
	keyboardGrab := flag.Bool("keyboard-grab", false, "XGrabKeyboard on the window once it is mapped")
	at := flag.String("at", "0,0", "window position X,Y in root coordinates")
	flag.Parse()
	atX, atY, okAt := strings.Cut(*at, ",")
	if !okAt {
		return fmt.Errorf("bad -at %q", *at)
	}
	w, h, ok := parseSize(*size)
	if !ok {
		return fmt.Errorf("bad -size %q", *size)
	}

	c, err := xturbo.Dial("") // $DISPLAY
	if err != nil {
		return err
	}
	defer c.Close()

	var atoms [9]uint32
	if err := c.InternAtoms([]string{"WM_PROTOCOLS", "WM_DELETE_WINDOW", "_NET_WM_NAME", "UTF8_STRING",
		"_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_DROPDOWN_MENU", "_NET_WM_WINDOW_TYPE_TOOLTIP", "_NET_WM_STATE", "_NET_WM_STATE_FULLSCREEN"}, false, atoms[:]); err != nil {
		return err
	}
	protocols, deleteWin, netWMName, utf8 := xproto.Atom(atoms[0]), xproto.Atom(atoms[1]), xproto.Atom(atoms[2]), xproto.Atom(atoms[3])
	typeAtom, typeMenu, typeTooltip := xproto.Atom(atoms[4]), atoms[5], atoms[6]
	netWMState, netWMFullscreen := atoms[7], atoms[8]

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
	v.SetEventMask(xproto.EventMaskExposure | xproto.EventMaskStructureNotify | xproto.EventMaskFocusChange | xproto.EventMaskPropertyChange)
	xproto.CreateWindow(b, 0, win, xproto.Window(c.Root()), int16(atoi(atX)), int16(atoi(atY)), uint16(w), uint16(h), 0, xproto.WindowClassInputOutput, 0, &v)

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
	if *at != "0,0" {
		// Like games, ask for the rect before the map (a ConfigureRequest the
		// window manager sees as the output hint).
		var cv xproto.ConfigureWindowValues
		cv.SetX(int32(atoi(atX))).SetY(int32(atoi(atY))).SetWidth(uint32(w)).SetHeight(uint32(h))
		xproto.ConfigureWindow(b, win, &cv)
	}
	xproto.MapWindow(b, win)

	// The override-redirect window is created once the main window is mapped.
	var ovWin xproto.Window
	var ovAt, ovSz [2]int
	if *override {
		var ok1, ok2 bool
		x, y, found := strings.Cut(*overrideAt, ",")
		ovAt[0], ovAt[1] = atoi(x), atoi(y)
		ok1 = found
		ovSz[0], ovSz[1], ok2 = parseSize(*overrideSize)
		if !ok1 || !ok2 || (*overrideType != "menu" && *overrideType != "tooltip") {
			return fmt.Errorf("bad -override-at/-override-size/-override-type")
		}
	}
	var ovDeadline time.Time // zero: no timed unmap pending
	unmapOverride := func(msg string) {
		if ovWin == 0 {
			return
		}
		xproto.UnmapWindow(b, ovWin)
		ovWin = 0
		ovDeadline = time.Time{}
		fmt.Println(msg)
	}
	mapOverride := func() error {
		id, err := c.NewXID()
		if err != nil {
			return err
		}
		ovWin = xproto.Window(id)
		var ov xproto.CreateWindowValues
		ov.SetBackgroundPixel(magenta).SetOverrideRedirect(true)
		xproto.CreateWindow(b, 0, ovWin, xproto.Window(c.Root()), int16(ovAt[0]), int16(ovAt[1]), uint16(ovSz[0]), uint16(ovSz[1]), 0, xproto.WindowClassInputOutput, 0, &ov)
		typ := typeMenu
		if *overrideType == "tooltip" {
			typ = typeTooltip
		}
		var tb [4]byte
		binary.LittleEndian.PutUint32(tb[:], typ)
		xproto.ChangeProperty(b, xproto.PropModeReplace, ovWin, typeAtom, xproto.AtomAtom, 32, 1, tb[:])
		if *overrideShape {
			shape.Rectangles(b, shape.SoSet, shape.SkInput, 0, ovWin, 0, 0, nil)
		}
		xproto.MapWindow(b, ovWin)
		fmt.Println("override mapped")
		if *overrideFor > 0 {
			ovDeadline = time.Now().Add(*overrideFor)
		}
		return nil
	}
	var ovErr error

	// Fullscreen requests go to the root window as a client message, as ICCCM
	// and EWMH say for a mapped window.
	fsMsg := func(action uint32) {
		var ev [32]byte
		ev[0] = byte(xproto.KindClientMessage)
		ev[1] = 32
		binary.LittleEndian.PutUint32(ev[4:], uint32(win))
		binary.LittleEndian.PutUint32(ev[8:], netWMState)
		binary.LittleEndian.PutUint32(ev[12:], action)
		binary.LittleEndian.PutUint32(ev[16:], netWMFullscreen)
		xproto.SendEvent(b, false, xproto.Window(c.Root()), xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, &ev)
	}
	var fsAt, fsOffAt time.Time // zero: nothing pending

	mapped, done, isFS := false, false, false
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
					if *override {
						ovErr = mapOverride()
					}
					if *fullscreen {
						fsAt = time.Now().Add(*fullscreenDelay)
					}
					if *keyboardGrab {
						xproto.GrabKeyboard(b, true, win, 0, xproto.GrabModeAsync, xproto.GrabModeAsync)
						fmt.Println("grab")
					}
				}
			case xproto.KindPropertyNotify:
				if e := xproto.PropertyNotifyOf(ev); e.Window() == win && xproto.Atom(e.Atom()) == xproto.Atom(netWMState) {
					seq := xproto.GetProperty(b, false, win, xproto.Atom(netWMState), xproto.AtomAtom, 0, 64)
					if err := c.Expect(seq, 16, 0, 0, 0, 0); err != nil {
						ovErr = err
					}
				}
			case xproto.KindFocusIn:
				fmt.Println("focus")
			case xproto.KindFocusOut:
				if *dismiss && xproto.FocusOutOf(ev).Event() == win {
					unmapOverride("dismissed")
				}
			case xproto.KindClientMessage:
				e := xproto.ClientMessageOf(ev)
				if e.Type() == protocols && e.Format() == 32 && e.Data().Data32()[0] == uint32(deleteWin) {
					done = true
				}
			}
		},
		OnReply: func(r *xturbo.Reply) {
			rep, ok := xproto.ParseGetPropertyReply(r.Raw)
			if !ok {
				return
			}
			on := false
			for v := rep.Value(); len(v) >= 4; v = v[4:] {
				on = on || binary.LittleEndian.Uint32(v) == netWMFullscreen
			}
			// Report the state the window manager published, once per change.
			switch {
			case on && !isFS:
				fmt.Printf("fullscreen %dx%d\n", cw, ch)
			case !on && isFS:
				fmt.Printf("fullscreen off %dx%d\n", cw, ch)
			}
			isFS = on
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
		timeout := -1
		for _, d := range []time.Time{ovDeadline, fsAt, fsOffAt} {
			if !d.IsZero() {
				ms := max(int(time.Until(d).Milliseconds())+1, 0)
				if timeout < 0 || ms < timeout {
					timeout = ms
				}
			}
		}
		fds := []unix.PollFd{{Fd: int32(c.Fd()), Events: events}}
		if _, err := unix.Poll(fds, timeout); err != nil && err != unix.EINTR {
			return err
		}
		if !ovDeadline.IsZero() && !time.Now().Before(ovDeadline) {
			unmapOverride("override unmapped")
		}
		if !fsAt.IsZero() && !time.Now().Before(fsAt) {
			fsAt = time.Time{}
			fsMsg(1)
			if *fullscreenOff > 0 {
				fsOffAt = time.Now().Add(*fullscreenOff)
			}
		}
		if !fsOffAt.IsZero() && !time.Now().Before(fsOffAt) {
			fsOffAt = time.Time{}
			fsMsg(0)
		}
		if err := c.Dispatch(hd); err != nil {
			return err
		}
		if ovErr != nil {
			return ovErr
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

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
