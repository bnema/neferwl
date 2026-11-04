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
// With -initial-fullscreen the window maps already fullscreen and fixed-size.
// With -fullscreen it asks the window manager for _NET_WM_STATE_FULLSCREEN
// -fullscreen-delay after the window is mapped (0: right at the map) and
// prints "fullscreen WxH" when the window is resized; -fullscreen-off asks to
// leave it that long after (prints "fullscreen off WxH"). -keyboard-grab
// calls XGrabKeyboard on the window once it is mapped (prints "grab").
// -at X,Y creates the window at that root position. -focus-log also prints
// "focus-in"/"focus-out" for the window's own focus changes (pointer details
// skipped) and "active self|none|other" whenever the root's
// _NET_ACTIVE_WINDOW changes, and "key" for each key press it receives.
// -stdin-sync reads stdin lines: each "sync" makes a round trip to the X
// server and prints "synced N" (N counts syncs) once the reply arrives,
// after every event the server sent before it.
//
// Four more modes run without a window and exit when done.
// -copy TEXT|@file owns -copy-sel (CLIPBOARD or PRIMARY) for
// -copy-for, serving TARGETS and UTF8_STRING/STRING/TEXT, with INCR above
// 64 KiB, and prints "copy owned". -paste SEL requests UTF8_STRING (INCR
// aware) and prints `paste "<text>"`, `paste len=N sha256=H` above 4 KiB or
// `paste none`. -xsettings prints the XSETTINGS owner's serial, Xft/DPI, Gdk/WindowScalingFactor, Gdk/UnscaledDPI,
// Gtk/CursorThemeName and Gtk/CursorThemeSize, or "xsettings none".
// -root-cursor prints "cursor WxH" of the XFixes cursor image.
package main

import (
	"crypto/sha256"
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
	"bnema.dev/xturbo/xfixes"
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
	initialFullscreen := flag.Bool("initial-fullscreen", false, "set _NET_WM_STATE_FULLSCREEN and fixed-size WM_NORMAL_HINTS (min=max=size) before mapping")
	keyboardGrab := flag.Bool("keyboard-grab", false, "XGrabKeyboard on the window once it is mapped")
	focusLog := flag.Bool("focus-log", false, "print focus-in/focus-out and _NET_ACTIVE_WINDOW changes")
	stdinSync := flag.Bool("stdin-sync", false, `answer each stdin line "sync" with "synced N" after an X round trip`)
	at := flag.String("at", "0,0", "window position X,Y in root coordinates")
	copyText := flag.String("copy", "", "own a selection with TEXT (or @file) and serve it, no window")
	copySel := flag.String("copy-sel", "CLIPBOARD", "selection for -copy: CLIPBOARD or PRIMARY")
	copyFor := flag.Duration("copy-for", 5*time.Second, "how long -copy serves the selection")
	paste := flag.String("paste", "", "print the text of selection CLIPBOARD or PRIMARY, no window")
	xsettings := flag.Bool("xsettings", false, "print the XSETTINGS values, no window")
	rootCursor := flag.Bool("root-cursor", false, "print the size of the cursor shown over the root, no window")
	flag.Parse()
	if *copyText != "" || *paste != "" || *xsettings || *rootCursor {
		return runTool(*copyText, *copySel, *copyFor, *paste, *xsettings, *rootCursor)
	}
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

	var atoms [10]uint32
	if err := c.InternAtoms([]string{"WM_PROTOCOLS", "WM_DELETE_WINDOW", "_NET_WM_NAME", "UTF8_STRING",
		"_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_DROPDOWN_MENU", "_NET_WM_WINDOW_TYPE_TOOLTIP", "_NET_WM_STATE", "_NET_WM_STATE_FULLSCREEN", "_NET_ACTIVE_WINDOW"}, false, atoms[:]); err != nil {
		return err
	}
	netActive := xproto.Atom(atoms[9])
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

	// Reply kinds: 16 is _NET_WM_STATE, 17 the root's _NET_ACTIVE_WINDOW.
	const kindState, kindActive, kindSync = 16, 17, 18
	syncs := 0
	var stdinBuf []byte
	// readStdin issues a round trip for each complete "sync" line.
	readStdin := func() error {
		var buf [256]byte
		n, err := unix.Read(0, buf[:])
		if n <= 0 {
			if err == unix.EAGAIN || err == unix.EINTR {
				return nil
			}
			*stdinSync = false // EOF: stop polling stdin
			return nil
		}
		stdinBuf = append(stdinBuf, buf[:n]...)
		for {
			i := strings.IndexByte(string(stdinBuf), '\n')
			if i < 0 {
				return nil
			}
			line := strings.TrimSpace(string(stdinBuf[:i]))
			stdinBuf = stdinBuf[i+1:]
			if line == "sync" {
				if err := c.Expect(xproto.GetInputFocus(b), kindSync, 0, 0, 0, 0); err != nil {
					return err
				}
			}
		}
	}
	root := xproto.Window(c.Root())
	queryActive := func() {
		seq := xproto.GetProperty(b, false, root, netActive, xproto.AtomWindow, 0, 1)
		if err := c.Expect(seq, kindActive, 0, 0, 0, 0); err != nil {
			fmt.Fprintln(os.Stderr, "x11probe:", err)
		}
	}
	if *focusLog {
		var rv xproto.ChangeWindowAttributesValues
		rv.SetEventMask(xproto.EventMaskPropertyChange)
		xproto.ChangeWindowAttributes(b, root, &rv)
		var wv xproto.ChangeWindowAttributesValues
		wv.SetEventMask(xproto.EventMaskExposure | xproto.EventMaskStructureNotify | xproto.EventMaskFocusChange | xproto.EventMaskPropertyChange | xproto.EventMaskKeyPress)
		xproto.ChangeWindowAttributes(b, win, &wv)
	}

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
	if *initialFullscreen {
		// Like a Proton game: fullscreen state and a fixed size, both set
		// before the map.
		var st [4]byte
		binary.LittleEndian.PutUint32(st[:], netWMFullscreen)
		xproto.ChangeProperty(b, xproto.PropModeReplace, win, xproto.Atom(netWMState), xproto.AtomAtom, 32, 1, st[:])
		var nh [72]byte                              // WM_SIZE_HINTS: flags, x, y, w, h, min, max, ...
		binary.LittleEndian.PutUint32(nh[0:], 16|32) // PMinSize|PMaxSize
		for _, off := range []int{20, 28} {
			binary.LittleEndian.PutUint32(nh[off:], uint32(w))
			binary.LittleEndian.PutUint32(nh[off+4:], uint32(h))
		}
		xproto.ChangeProperty(b, xproto.PropModeReplace, win, xproto.AtomWmNormalHints, xproto.AtomWmSizeHints, 32, 18, nh[:])
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
					if err := c.Expect(seq, kindState, 0, 0, 0, 0); err != nil {
						ovErr = err
					}
				}
				if e := xproto.PropertyNotifyOf(ev); *focusLog && e.Window() == root && xproto.Atom(e.Atom()) == netActive {
					queryActive()
				}
			case xproto.KindKeyPress:
				fmt.Println("key")
			case xproto.KindFocusIn:
				fmt.Println("focus")
				if e := xproto.FocusInOf(ev); *focusLog && e.Event() == win && e.Detail() != xproto.NotifyDetailPointer {
					fmt.Println("focus-in")
				}
			case xproto.KindFocusOut:
				if e := xproto.FocusOutOf(ev); *focusLog && e.Event() == win && e.Detail() != xproto.NotifyDetailPointer {
					fmt.Println("focus-out")
				}
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
			if r.Kind == kindSync {
				syncs++
				fmt.Printf("synced %d\n", syncs)
				return
			}
			if r.Kind == kindActive {
				var id uint32
				if v := rep.Value(); len(v) >= 4 {
					id = binary.LittleEndian.Uint32(v)
				}
				switch id {
				case uint32(win):
					fmt.Println("active self")
				case 0:
					fmt.Println("active none")
				default:
					fmt.Println("active other")
				}
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
		if *stdinSync {
			fds = append(fds, unix.PollFd{Fd: 0, Events: unix.POLLIN})
		}
		if _, err := unix.Poll(fds, timeout); err != nil && err != unix.EINTR {
			return err
		}
		if len(fds) > 1 && fds[1].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
			if err := readStdin(); err != nil {
				return err
			}
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

// tool is the connection of the windowless modes. Events read while waiting
// for a reply are queued, so the code reads linearly.
type tool struct {
	c     *xturbo.Conn
	b     *xturbo.Buf
	win   xproto.Window
	q     []xturbo.Event
	reply []byte
	h     xturbo.Handler
	atoms map[string]xproto.Atom
}

const chunk = 65536

func newTool() (*tool, error) {
	c, err := xturbo.Dial("")
	if err != nil {
		return nil, err
	}
	t := &tool{c: c, b: c.Buf(), atoms: map[string]xproto.Atom{}}
	t.h = xturbo.Handler{
		OnEvent: func(ev *xturbo.Event) { t.q = append(t.q, *ev) },
		OnReply: func(r *xturbo.Reply) { t.reply = append(t.reply[:0], r.Raw...) },
		OnError: func(e *xturbo.XError) { fmt.Fprintln(os.Stderr, "x11probe:", e) },
	}
	names := []string{"CLIPBOARD", "PRIMARY", "TARGETS", "UTF8_STRING", "STRING", "TEXT", "INCR", "_PROBE_SEL",
		"_XSETTINGS_S0", "_XSETTINGS_SETTINGS"}
	ids := make([]uint32, len(names))
	if err := c.InternAtoms(names, false, ids); err != nil {
		return nil, err
	}
	for i, n := range names {
		t.atoms[n] = xproto.Atom(ids[i])
	}
	xid, err := c.NewXID()
	if err != nil {
		return nil, err
	}
	t.win = xproto.Window(xid)
	var v xproto.CreateWindowValues
	v.SetEventMask(xproto.EventMaskPropertyChange)
	xproto.CreateWindow(t.b, 0, t.win, xproto.Window(c.Root()), -10, -10, 1, 1, 0, xproto.WindowClassInputOnly, 0, &v)
	return t, t.c.Sync(&t.h)
}

// rpc sends the queued requests and returns the reply of seq.
func (t *tool) rpc(seq xturbo.Seq) ([]byte, error) {
	t.reply = t.reply[:0]
	if err := t.c.Expect(seq, 16, 0, 0, 0, 0); err != nil {
		return nil, err
	}
	if err := t.c.Sync(&t.h); err != nil {
		return nil, err
	}
	return t.reply, nil
}

// next returns the next event, or false at the deadline.
func (t *tool) next(deadline time.Time) (xturbo.Event, bool, error) {
	for len(t.q) == 0 {
		events := int16(unix.POLLIN)
		if err := t.c.Flush(); err == xturbo.ErrWouldBlock {
			events |= unix.POLLOUT
		} else if err != nil {
			return xturbo.Event{}, false, err
		}
		ms := int(time.Until(deadline).Milliseconds()) + 1
		if ms <= 0 {
			return xturbo.Event{}, false, nil
		}
		fds := []unix.PollFd{{Fd: int32(t.c.Fd()), Events: events}}
		if _, err := unix.Poll(fds, ms); err != nil && err != unix.EINTR {
			return xturbo.Event{}, false, err
		}
		if err := t.c.Dispatch(&t.h); err != nil {
			return xturbo.Event{}, false, err
		}
	}
	ev := t.q[0]
	t.q = t.q[1:]
	return ev, true, nil
}

func (t *tool) selAtom(name string) (xproto.Atom, error) {
	switch name {
	case "CLIPBOARD", "PRIMARY":
		return t.atoms[name], nil
	}
	return 0, fmt.Errorf("bad selection %q", name)
}

func runTool(copyText, copySel string, copyFor time.Duration, paste string, xsettings, rootCursor bool) error {
	t, err := newTool()
	if err != nil {
		return err
	}
	defer t.c.Close()
	switch {
	case copyText != "":
		return t.copyMode(copyText, copySel, copyFor)
	case paste != "":
		return t.pasteMode(paste)
	case xsettings:
		return t.xsettingsMode()
	}
	return t.cursorMode()
}

func (t *tool) copyMode(text, selName string, dur time.Duration) error {
	data := []byte(text)
	if f, ok := strings.CutPrefix(text, "@"); ok {
		var err error
		if data, err = os.ReadFile(f); err != nil {
			return err
		}
	}
	sel, err := t.selAtom(selName)
	if err != nil {
		return err
	}
	xproto.SetSelectionOwner(t.b, t.win, sel, 0)
	raw, err := t.rpc(xproto.GetSelectionOwner(t.b, sel))
	if err != nil {
		return err
	}
	if r, ok := xproto.ParseGetSelectionOwnerReply(raw); !ok || r.Owner() != t.win {
		return fmt.Errorf("selection %s not owned", selName)
	}
	fmt.Println("copy owned")
	var (
		incrReq           xproto.Window // requestor of the running INCR transfer
		incrProp, incrTgt xproto.Atom
		incrOff           int
		incrLive          bool
		notify            = func(req xproto.Window, selection, target, prop xproto.Atom, time xproto.Timestamp) {
			var ev [32]byte
			d := xproto.SelectionNotifyEventData{Time: time, Requestor: req, Selection: selection, Target: target, Property: prop}
			d.Put(&ev)
			xproto.SendEvent(t.b, false, req, 0, &ev)
		}
	)
	deadline := time.Now().Add(dur)
	for {
		ev, ok, err := t.next(deadline)
		if err != nil || !ok {
			return err
		}
		switch ev.Kind {
		case xproto.KindSelectionClear:
			fmt.Println("copy lost")
			return nil
		case xproto.KindSelectionRequest:
			e := xproto.SelectionRequestOf(&ev)
			req, prop, tgt := e.Requestor(), e.Property(), e.Target()
			if prop == 0 {
				prop = tgt
			}
			switch tgt {
			case t.atoms["TARGETS"]:
				var l [16]byte
				for i, n := range []string{"TARGETS", "UTF8_STRING", "STRING", "TEXT"} {
					binary.LittleEndian.PutUint32(l[i*4:], uint32(t.atoms[n]))
				}
				xproto.ChangeProperty(t.b, xproto.PropModeReplace, req, prop, xproto.AtomAtom, 32, 4, l[:])
			case t.atoms["UTF8_STRING"], t.atoms["STRING"], t.atoms["TEXT"]:
				if len(data) <= chunk {
					xproto.ChangeProperty(t.b, xproto.PropModeReplace, req, prop, tgt, 8, uint32(len(data)), data)
					break
				}
				var cv xproto.ChangeWindowAttributesValues
				cv.SetEventMask(xproto.EventMaskPropertyChange)
				xproto.ChangeWindowAttributes(t.b, req, &cv)
				var n [4]byte
				binary.LittleEndian.PutUint32(n[:], uint32(len(data)))
				xproto.ChangeProperty(t.b, xproto.PropModeReplace, req, prop, t.atoms["INCR"], 32, 1, n[:])
				incrReq, incrProp, incrTgt, incrOff, incrLive = req, prop, tgt, 0, true
			default:
				prop = 0
			}
			notify(req, e.Selection(), tgt, prop, e.Time())
		case xproto.KindPropertyNotify:
			e := xproto.PropertyNotifyOf(&ev)
			if !incrLive || e.Window() != incrReq || e.Atom() != incrProp || e.State() != xproto.PropertyDelete {
				break
			}
			end := min(incrOff+chunk, len(data))
			xproto.ChangeProperty(t.b, xproto.PropModeReplace, incrReq, incrProp, incrTgt, 8, uint32(end-incrOff), data[incrOff:end])
			if end == incrOff {
				incrLive = false
			}
			incrOff = end
		}
	}
}

func (t *tool) pasteMode(selName string) error {
	sel, err := t.selAtom(selName)
	if err != nil {
		return err
	}
	prop := t.atoms["_PROBE_SEL"]
	xproto.ConvertSelection(t.b, t.win, sel, t.atoms["UTF8_STRING"], prop, 0)
	deadline := time.Now().Add(10 * time.Second)
	var got []byte
	incr := false
	get := func() (value []byte, typ xproto.Atom, err error) {
		raw, err := t.rpc(xproto.GetProperty(t.b, true, t.win, prop, xproto.AtomAny, 0, 1<<22))
		if err != nil {
			return nil, 0, err
		}
		r, ok := xproto.ParseGetPropertyReply(raw)
		if !ok {
			return nil, 0, fmt.Errorf("bad GetProperty reply")
		}
		return append([]byte(nil), r.Value()...), r.Type(), nil
	}
	for {
		ev, ok, err := t.next(deadline)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("paste timed out")
		}
		switch ev.Kind {
		case xproto.KindSelectionNotify:
			if xproto.SelectionNotifyOf(&ev).Property() == 0 {
				fmt.Println("paste none")
				return nil
			}
			v, typ, err := get()
			if err != nil {
				return err
			}
			if typ != t.atoms["INCR"] {
				return printPaste(v)
			}
			incr = true // reading and deleting the INCR property starts the transfer
		case xproto.KindPropertyNotify:
			e := xproto.PropertyNotifyOf(&ev)
			if !incr || e.Window() != t.win || e.Atom() != prop || e.State() != xproto.PropertyNewValue {
				break
			}
			v, _, err := get()
			if err != nil {
				return err
			}
			if len(v) == 0 {
				return printPaste(got)
			}
			got = append(got, v...)
		}
		deadline = time.Now().Add(10 * time.Second)
	}
}

func printPaste(v []byte) error {
	if len(v) <= 4096 {
		fmt.Printf("paste %q\n", v)
	} else {
		fmt.Printf("paste len=%d sha256=%x\n", len(v), sha256.Sum256(v))
	}
	return nil
}

func (t *tool) xsettingsMode() error {
	raw, err := t.rpc(xproto.GetSelectionOwner(t.b, t.atoms["_XSETTINGS_S0"]))
	if err != nil {
		return err
	}
	r, ok := xproto.ParseGetSelectionOwnerReply(raw)
	if !ok || r.Owner() == 0 {
		fmt.Println("xsettings none")
		return nil
	}
	raw, err = t.rpc(xproto.GetProperty(t.b, false, r.Owner(), t.atoms["_XSETTINGS_SETTINGS"], xproto.AtomAny, 0, 1<<16))
	if err != nil {
		return err
	}
	pr, ok := xproto.ParseGetPropertyReply(raw)
	if !ok || len(pr.Value()) < 12 {
		fmt.Println("xsettings none")
		return nil
	}
	v := pr.Value()
	if v[0] != 0 {
		return fmt.Errorf("xsettings: big-endian data")
	}
	le := binary.LittleEndian
	serial, n := le.Uint32(v[4:]), int(le.Uint32(v[8:]))
	vals := map[string]string{}
	p := v[12:]
	pad := func(n int) int { return (n + 3) &^ 3 }
	for i := 0; i < n; i++ {
		if len(p) < 4 {
			return fmt.Errorf("xsettings: truncated")
		}
		typ, nl := p[0], int(le.Uint16(p[2:]))
		if len(p) < 4+pad(nl)+4 {
			return fmt.Errorf("xsettings: truncated")
		}
		name := string(p[4 : 4+nl])
		p = p[4+pad(nl)+4:] // name, last-change-serial
		switch typ {
		case 0:
			vals[name] = strconv.Itoa(int(int32(le.Uint32(p))))
			p = p[4:]
		case 1:
			sl := int(le.Uint32(p))
			vals[name] = string(p[4 : 4+sl])
			p = p[4+pad(sl):]
		case 2:
			p = p[8:]
		default:
			return fmt.Errorf("xsettings: type %d", typ)
		}
	}
	fmt.Printf("xsettings serial=%d Xft/DPI=%s Gdk/WindowScalingFactor=%s Gdk/UnscaledDPI=%s Gtk/CursorThemeName=%s Gtk/CursorThemeSize=%s\n",
		serial, vals["Xft/DPI"], vals["Gdk/WindowScalingFactor"], vals["Gdk/UnscaledDPI"], vals["Gtk/CursorThemeName"], vals["Gtk/CursorThemeSize"])
	return nil
}

func (t *tool) cursorMode() error {
	if _, err := t.rpc(xfixes.QueryVersion(t.b, 4, 0)); err != nil {
		return err
	}
	raw, err := t.rpc(xfixes.GetCursorImage(t.b))
	if err != nil {
		return err
	}
	r, ok := xfixes.ParseGetCursorImageReply(raw)
	if !ok {
		return fmt.Errorf("bad GetCursorImage reply")
	}
	fmt.Printf("cursor %dx%d\n", r.Width(), r.Height())
	return nil
}
