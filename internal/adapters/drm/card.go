package drm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"unsafe"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Card is one DRM device and the outputs it drives. Scan finds connected
// connectors; each gets its own Output (CRTC, buffers, cursor) run by one
// goroutine. One reader goroutine reads page-flip events for all of them
// and hands each to its output by CRTC.
type Card struct {
	fd    int
	path  string
	want  Want
	log   zerowrap.Logger
	crtcs []uint32
	// outputs by connector name; owned by the goroutine that calls Scan.
	outputs map[string]*Output
	flips   map[uint32]chan int // by CRTC; set before the reader starts
	// gemMu serialises GEM handle import and close on fd (outputs run on
	// their own goroutines); modifiers is DRM_CAP_ADDFB2_MODIFIERS.
	gemMu     sync.Mutex
	modifiers bool
}

// OpenCard reads the card's CRTCs. fd stays owned by the caller.
func OpenCard(fd int, path string, want Want, log zerowrap.Logger) (*Card, error) {
	crtcs, _, err := resources(fd)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	flips := make(map[uint32]chan int, len(crtcs))
	for _, c := range crtcs {
		flips[c] = make(chan int, 4)
	}
	cp := getCap{capability: capAddFB2Modifiers}
	mods := ioctl(fd, ioctlGetCap, unsafe.Pointer(&cp)) == nil && cp.value == 1
	return &Card{fd: fd, path: path, want: want, log: log, crtcs: crtcs, outputs: map[string]*Output{}, flips: flips, modifiers: mods}, nil
}

// Path is the device path, e.g. /dev/dri/card1.
func (c *Card) Path() string { return c.path }

// SetWant replaces the connector config; the next Scan applies it.
func (c *Card) SetWant(w Want) { c.want = w }

// Scan compares connectors with the outputs driven now. It returns outputs to
// start (newly connected, or with a new mode after Release), names of
// outputs to stop because they are gone (disconnected or disabled), and
// names to stop because they need a new mode. A stopped output must be
// closed by its goroutine before its CRTC is reused: call Release.
func (c *Card) Scan() (added []*Output, removed, replaced []string, err error) {
	_, ids, err := resources(c.fd)
	if err != nil {
		return nil, nil, nil, err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		conn, err := readConnector(c.fd, id)
		if err != nil {
			c.log.Warn().Err(err).Uint32("connector", id).Msg("read connector")
			continue
		}
		if !c.want.usable(conn) {
			continue
		}
		seen[conn.name] = true
		mode := c.want.pickMode(conn)
		if o := c.outputs[conn.name]; o != nil {
			if o.mode == mode {
				continue
			}
			replaced = append(replaced, conn.name)
			continue
		}
		o, err := c.open(conn, mode)
		if err != nil {
			c.log.Warn().Err(err).Str("connector", conn.name).Msg("output unusable")
			continue
		}
		c.outputs[conn.name] = o
		added = append(added, o)
	}
	for name := range c.outputs {
		if !seen[name] {
			removed = append(removed, name)
		}
	}
	slices.Sort(removed)
	slices.Sort(replaced)
	return added, removed, replaced, nil
}

// Release forgets an output closed by its goroutine, freeing its CRTC for the
// next Scan.
func (c *Card) Release(name string) { delete(c.outputs, name) }

// Outputs returns the driven outputs.
func (c *Card) Outputs() []*Output {
	out := make([]*Output, 0, len(c.outputs))
	for _, o := range c.outputs {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b *Output) int {
		if a.conn.name < b.conn.name {
			return -1
		}
		return 1
	})
	return out
}

func (c *Card) open(conn connector, mode modeInfo) (*Output, error) {
	used := map[uint32]bool{}
	for _, o := range c.outputs {
		used[o.crtc] = true
	}
	crtc, err := pickCrtc(c.fd, conn, c.crtcs, used)
	if err != nil {
		return nil, err
	}
	return newOutput(c, conn, mode, crtc)
}

// ReadEvents reads page-flip events until ctx ends and routes them to the
// output of their CRTC. Run it once per card.
func (c *Card) ReadEvents(ctx context.Context) error {
	buf := make([]byte, 1024)
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: int32(c.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("drm poll: %w", err)
		}
		if n <= 0 {
			continue
		}
		m, err := unix.Read(c.fd, buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return fmt.Errorf("drm read: %w", err)
		}
		for _, crtc := range flipCrtcs(buf[:m]) {
			ch := c.flips[crtc]
			if ch == nil {
				continue
			}
			select {
			case ch <- 1:
			default: // the output is busy; the flip is counted on its next event
			}
		}
	}
	return nil
}
