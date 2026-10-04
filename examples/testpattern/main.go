// Command testpattern is a minimal Wayland client that shows a grid of known
// colours, to check by eye or by screenshot that NeferWL renders them
// correctly:
//
//	WAYLAND_DISPLAY=wayland-1 go run ./examples/testpattern -mode hdr -fullscreen
//
// Modes:
//
//	sdr  XRGB8888 wl_shm buffer of sRGB patches, no colour management
//	hdr  XR30 linear dmabuf of BT.2020 PQ patches, declared with
//	     wp_color_manager_v1 (st2084_pq, bt2020); needs /dev/udmabuf
//
// The patches are defined in the pattern sub-package. The client prints
// "ready <w> <h>" on stderr once the window is mapped and drawn at its final
// size, and, in hdr mode, "output-tf <n>" first: the named transfer function
// of the compositor's output (11 is PQ, so virtual or real HDR is on; 0 when
// the output does not report one).
//
// With -fullscreen the window asks for fullscreen and "ready" waits for the
// compositor to grant it. NeferWL drops set_fullscreen sent before the
// surface is mapped and ignores client fullscreen requests during the first
// second after the map (core's fullscreenGrace), so the client maps first and
// sends the request 1.2 s after its first buffer commit.
//
// The client runs until the display closes, then exits 0.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/bnema/neferwl/examples/testpattern/pattern"
	"github.com/bnema/wlturbo"
	"github.com/bnema/wlturbo/protocol/colormanagement"
	clientcore "github.com/bnema/wlturbo/protocol/core"
	"github.com/bnema/wlturbo/protocol/linuxdmabuf"
	"github.com/bnema/wlturbo/protocol/xdgshell"
	"golang.org/x/sys/unix"
)

const (
	defaultWidth, defaultHeight = 640, 360
	// fullscreenDelay outlasts core's 1 s fullscreenGrace.
	fullscreenDelay = 1200 * time.Millisecond

	shmXRGB8888 = 1
	fourccXR30  = 'X' | 'R'<<8 | '3'<<16 | '0'<<24
)

func main() {
	mode := flag.String("mode", "sdr", "patch set: sdr or hdr")
	fullscreen := flag.Bool("fullscreen", false, "ask for fullscreen after mapping")
	flag.Parse()
	if *mode != "sdr" && *mode != "hdr" {
		fmt.Fprintf(os.Stderr, "testpattern: unknown mode %q (want sdr or hdr)\n", *mode)
		os.Exit(2)
	}
	if err := run(*mode, *fullscreen); err != nil {
		fmt.Fprintln(os.Stderr, "testpattern:", err)
		os.Exit(1)
	}
}

// client holds the objects and state of one window. Only the dispatch
// goroutine touches the state; the fullscreen timer only sends a request.
type client struct {
	d          *wlturbo.Display
	mode       string
	fullscreen bool

	surface *clientcore.Surface
	top     *xdgshell.XdgToplevel
	shm     *clientcore.Shm
	dmabuf  *linuxdmabuf.LinuxDmabuf

	// pending toplevel configure, applied by the xdg_surface configure.
	pendW, pendH int32
	pendFull     bool

	w, h     int
	buffer   *clientcore.Buffer
	ready    bool
	closed   bool
	err      error
	xr30Lin  bool // XR30 with modifier 0 advertised
	timerSet bool
}

func run(mode string, fullscreen bool) error {
	d, err := wlturbo.Connect("")
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Roundtrip(); err != nil {
		return err
	}
	c := &client{d: d, mode: mode, fullscreen: fullscreen}
	bind := func(iface string, version uint32, proxy wlturbo.Proxy) error {
		_, err := d.Registry().BindNegotiated(iface, version, proxy)
		return err
	}
	compositor := clientcore.NewCompositor(d.Context())
	if err := bind(clientcore.CompositorInterface, 4, compositor); err != nil {
		return err
	}
	wm := xdgshell.NewXdgWmBase(d.Context())
	if err := bind(xdgshell.XdgWmBaseInterface, 1, wm); err != nil {
		return err
	}
	wm.OnPing(func(serial uint32) { _ = wm.Pong(serial) })

	var colour *colormanagement.WpColorManager
	var desc *colormanagement.WpImageDescription
	if mode == "sdr" {
		c.shm = clientcore.NewShm(d.Context())
		if err := bind(clientcore.ShmInterface, 1, c.shm); err != nil {
			return err
		}
		c.shm.OnFormat(func(uint32) {})
	} else {
		c.dmabuf = linuxdmabuf.NewLinuxDmabuf(d.Context())
		if v, err := d.Registry().BindNegotiated(linuxdmabuf.LinuxDmabufInterface, 3, c.dmabuf); err != nil {
			return err
		} else if v < 3 {
			return errors.New("linear XR30 unsupported: zwp_linux_dmabuf_v1 older than version 3")
		}
		c.dmabuf.OnFormat(func(uint32) {})
		c.dmabuf.OnModifier(func(format, hi, lo uint32) {
			if format == fourccXR30 && hi == 0 && lo == 0 {
				c.xr30Lin = true
			}
		})
		colour = colormanagement.NewWpColorManager(d.Context())
		if err := bind(colormanagement.WpColorManagerInterface, 2, colour); err != nil {
			return err
		}
		if err := d.Roundtrip(); err != nil {
			return err
		}
		if !c.xr30Lin {
			return errors.New("linear XR30 unsupported")
		}
		if desc, err = c.describe(colour); err != nil {
			return err
		}
		if err := c.printOutputTF(colour); err != nil {
			return err
		}
	}

	if c.surface, err = compositor.CreateSurface(); err != nil {
		return err
	}
	if desc != nil {
		cs, err := colour.GetSurface(c.surface)
		if err != nil {
			return err
		}
		if err := cs.SetImageDescription(desc, 0); err != nil {
			return err
		}
	}
	xs, err := wm.GetXdgSurface(c.surface)
	if err != nil {
		return err
	}
	if c.top, err = xs.GetToplevel(); err != nil {
		return err
	}
	_ = c.top.SetTitle("testpattern")
	_ = c.top.SetAppId("testpattern")
	c.top.OnConfigure(func(w, h int32, states []byte) {
		c.pendW, c.pendH, c.pendFull = w, h, hasState(states, xdgshell.STATE_FULLSCREEN)
	})
	c.top.OnClose(func() { c.closed = true })
	xs.OnConfigure(func(serial uint32) {
		if err := xs.AckConfigure(serial); err != nil {
			c.err = err
			return
		}
		if err := c.configured(); err != nil {
			c.err = err
		}
	})
	if err := c.surface.Commit(); err != nil { // initial commit asks for the first configure
		return err
	}
	for !c.closed && c.err == nil {
		if err := d.Dispatch(); err != nil {
			if isClosed(err) {
				return nil
			}
			return err
		}
	}
	return c.err
}

// describe creates the PQ/BT.2020 image description and waits until it is
// ready: a surface may only use a ready description.
func (c *client) describe(colour *colormanagement.WpColorManager) (*colormanagement.WpImageDescription, error) {
	creator, err := colour.CreateParametricCreator()
	if err != nil {
		return nil, err
	}
	if err := creator.SetTfNamed(colormanagement.TRANSFER_FUNCTION_ST2084_PQ); err != nil {
		return nil, err
	}
	if err := creator.SetPrimariesNamed(colormanagement.PRIMARIES_BT2020); err != nil {
		return nil, err
	}
	desc, err := creator.Create()
	if err != nil {
		return nil, err
	}
	ready, failed := false, ""
	desc.OnReady2(func(uint32, uint32) { ready = true })
	desc.OnFailed(func(cause uint32, msg string) { failed = fmt.Sprintf("cause %d: %s", cause, msg) })
	if err := c.until(func() bool { return ready || failed != "" }); err != nil {
		return nil, err
	}
	if failed != "" {
		return nil, fmt.Errorf("image description failed: %s", failed)
	}
	return desc, nil
}

// printOutputTF reports the named transfer function of the first output on
// stderr as "output-tf <n>" (0 when it reports none).
func (c *client) printOutputTF(colour *colormanagement.WpColorManager) error {
	g, ok := c.d.Registry().FindGlobal(clientcore.OutputInterface)
	if !ok {
		return errors.New("compositor has no wl_output")
	}
	output := clientcore.NewOutput(c.d.Context())
	if err := c.d.Registry().Bind(g.Name, g.Interface, min(g.Version, 4), output); err != nil {
		return err
	}
	co, err := colour.GetOutput(output)
	if err != nil {
		return err
	}
	desc, err := co.GetImageDescription()
	if err != nil {
		return err
	}
	ready, failed := false, false
	desc.OnReady2(func(uint32, uint32) { ready = true })
	desc.OnFailed(func(uint32, string) { failed = true })
	if err := c.until(func() bool { return ready || failed }); err != nil {
		return err
	}
	if failed {
		return errors.New("output image description failed")
	}
	info, err := desc.GetInformation()
	if err != nil {
		return err
	}
	tf, done := uint32(0), false
	info.OnTfNamed(func(v uint32) { tf = v })
	info.OnDone(func() { done = true })
	if err := c.until(func() bool { return done }); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "output-tf %d\n", tf)
	return nil
}

// until roundtrips until cond holds; setup events arrive within a few.
func (c *client) until(cond func() bool) error {
	for range 20 {
		if cond() {
			return nil
		}
		if err := c.d.Roundtrip(); err != nil {
			return err
		}
	}
	if cond() {
		return nil
	}
	return errors.New("compositor did not answer")
}

// configured applies a toplevel configure after it was acknowledged: it
// (re)builds the buffer when the size changed, commits, and reports ready.
func (c *client) configured() error {
	w, h := int(c.pendW), int(c.pendH)
	if w <= 0 || h <= 0 {
		w, h = c.w, c.h // the client picks the size
		if w == 0 || h == 0 {
			w, h = defaultWidth, defaultHeight
		}
	}
	if c.buffer == nil || w != c.w || h != c.h {
		buf, err := c.newBuffer(w, h)
		if err != nil {
			return err
		}
		old := c.buffer
		c.buffer, c.w, c.h = buf, w, h
		defer func() {
			if old != nil {
				_ = old.Destroy()
			}
		}()
	}
	if err := c.surface.Attach(c.buffer, 0, 0); err != nil {
		return err
	}
	if err := c.surface.Damage(0, 0, int32(c.w), int32(c.h)); err != nil {
		return err
	}
	if err := c.surface.Commit(); err != nil {
		return err
	}
	if c.fullscreen && !c.timerSet {
		c.timerSet = true
		top := c.top
		time.AfterFunc(fullscreenDelay, func() {
			if err := top.SetFullscreen(nil); err != nil {
				fmt.Fprintf(os.Stderr, "testpattern: set_fullscreen: %v\n", err)
			}
		})
	}
	if !c.ready && (!c.fullscreen || c.pendFull) {
		c.ready = true
		fmt.Fprintf(os.Stderr, "ready %d %d\n", c.w, c.h)
	}
	return nil
}

func (c *client) newBuffer(w, h int) (*clientcore.Buffer, error) {
	patches := pattern.Patches(c.mode, w, h)
	if c.mode == "sdr" {
		return c.shmBuffer(w, h, patches)
	}
	return c.dmabufBuffer(w, h, patches)
}

// shmBuffer draws the sRGB patches into XRGB8888 (little-endian B, G, R, X).
func (c *client) shmBuffer(w, h int, patches []pattern.Patch) (*clientcore.Buffer, error) {
	size := w * h * 4
	fd, err := wlturbo.CreateAnonymousFile(int64(size))
	if err != nil {
		return nil, err
	}
	data, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	for _, p := range patches {
		for y := p.Rect.Min.Y; y < p.Rect.Max.Y; y++ {
			for x := p.Rect.Min.X; x < p.Rect.Max.X; x++ {
				i := (y*w + x) * 4
				data[i], data[i+1], data[i+2], data[i+3] = p.SRGB[2], p.SRGB[1], p.SRGB[0], 0xff
			}
		}
	}
	// The compositor maps the file itself; ours is not needed any more.
	_ = unix.Munmap(data)
	pool, err := c.shm.CreatePool(fd, int32(size)) // closes fd once sent
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	buf, err := pool.CreateBuffer(0, int32(w), int32(h), int32(w*4), shmXRGB8888)
	_ = pool.Destroy()
	return buf, err
}

// dmabufBuffer draws the PQ patches into a udmabuf as XR30 words
// b | g<<10 | r<<20.
func (c *client) dmabufBuffer(w, h int, patches []pattern.Patch) (*clientcore.Buffer, error) {
	code := func(nits float64) uint32 { return uint32(math.Round(pattern.PQEncode(nits) * 1023)) }
	f, err := udmabuf(w*h*4, func(data []byte) {
		for _, p := range patches {
			word := code(p.Nits[2]) | code(p.Nits[1])<<10 | code(p.Nits[0])<<20
			for y := p.Rect.Min.Y; y < p.Rect.Max.Y; y++ {
				for x := p.Rect.Min.X; x < p.Rect.Max.X; x++ {
					i := (y*w + x) * 4
					data[i], data[i+1], data[i+2], data[i+3] = byte(word), byte(word>>8), byte(word>>16), byte(word>>24)
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	defer f.Close()
	params, err := c.dmabuf.CreateParams()
	if err != nil {
		return nil, err
	}
	// Add sends a duplicate: the send closes the descriptor it is given.
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	if err := params.Add(fd, 0, 0, uint32(w*4), 0, 0); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	buf, err := params.CreateImmed(int32(w), int32(h), fourccXR30, 0)
	_ = params.Destroy()
	return buf, err
}

func hasState(states []byte, want uint32) bool {
	for i := 0; i+4 <= len(states); i += 4 {
		if uint32(states[i])|uint32(states[i+1])<<8|uint32(states[i+2])<<16|uint32(states[i+3])<<24 == want {
			return true
		}
	}
	return false
}

// isClosed reports whether err is the compositor going away.
func isClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, net.ErrClosed)
}
