package drm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Card is one DRM device and the outputs it drives. Scan finds connected
// connectors; each gets its own Output (CRTC, planes, cursor) run by one
// goroutine. One reader goroutine reads commit events for all of them
// and hands each to its output by CRTC.
type Card struct {
	fd    int
	path  string
	want  Want
	log   zerowrap.Logger
	crtcs []uint32
	// outputs by connector name; owned by the goroutine that calls Scan.
	outputs map[string]*Output
	flips   map[uint32]chan flipEvent // by CRTC; set before the reader starts
	k       kmsDevice
	// taken are planes driven by an output, by plane ID.
	taken map[uint32]bool
	// async is DRM_CAP_ATOMIC_ASYNC_PAGE_FLIP.
	async bool
	// serials numbers the commits of every output (event userData).
	serials atomic.Uint64
	// formats receives each output's direct scanout formats (nil: none
	// sent); set before the first Scan.
	formats chan<- ports.OutputFormats
}

// OpenCard turns on atomic modesetting and reads the card's CRTCs. fd
// stays owned by the caller. A card without atomic KMS is refused.
func OpenCard(fd int, path string, want Want, log zerowrap.Logger) (*Card, error) {
	if err := enableAtomic(fd); err != nil {
		return nil, fmt.Errorf("drm: atomic modesetting unsupported by %s: %w", path, err)
	}
	crtcs, _, err := resources(fd)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	flips := make(map[uint32]chan flipEvent, len(crtcs))
	for _, c := range crtcs {
		flips[c] = make(chan flipEvent, 4)
	}
	k := kmsDevice{fd: fd, gemMu: &sync.Mutex{}, modifiers: hasCap(fd, capAddFB2Modifiers)}
	return &Card{fd: fd, path: path, want: want, log: log, crtcs: crtcs, outputs: map[string]*Output{}, flips: flips, k: k, taken: map[uint32]bool{}, async: hasCap(fd, capAtomicAsync)}, nil
}

// Path is the device path, e.g. /dev/dri/card1.
func (c *Card) Path() string { return c.path }

// Device is the card's dev_t, the KMS device clients allocate scanout
// buffers for (0 when unknown).
func (c *Card) Device() uint64 {
	var st unix.Stat_t
	if unix.Fstat(c.fd, &st) != nil {
		return 0
	}
	return st.Rdev
}

// SetFormats sets where outputs report their direct scanout formats.
// Call before the first Scan.
func (c *Card) SetFormats(ch chan<- ports.OutputFormats) { c.formats = ch }

// SetWant replaces the connector config; the next Scan applies it.
func (c *Card) SetWant(w Want) {
	c.want = w
}

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
			if !outputNeedsRestart(o, mode, c.want.HDR[conn.name]) {
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

// Release forgets an output closed by its goroutine, freeing its CRTC and
// planes for the next Scan.
func (c *Card) Release(name string) {
	if o := c.outputs[name]; o != nil {
		for _, p := range o.owned() {
			delete(c.taken, p.id)
		}
	}
	delete(c.outputs, name)
}

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
	o, err := newOutput(c, conn, mode, crtc)
	if err != nil {
		return nil, err
	}
	for _, p := range o.owned() {
		c.taken[p.id] = true
	}
	return o, nil
}

// ReadEvents reads commit events until ctx ends and routes them to the
// output of their CRTC. Run it once per card. An event is never dropped:
// the output owes it a pending commit.
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
		for _, ev := range parseFlips(buf[:m]) {
			ch := c.flips[ev.crtc]
			if ch == nil {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return nil
			}
		}
	}
	return nil
}

func normalizedHDRSettings(settings HDRSettings) HDRSettings {
	if settings.SDRBrightness == 0 {
		settings.SDRBrightness = ports.DefaultSDRBrightness
	}
	return settings
}

// outputNeedsRestart applies the same replacement path to mode and HDR
// configuration changes; Release drops the old output before reopening it.
func outputNeedsRestart(o *Output, mode modeInfo, settings HDRSettings) bool {
	return o.mode != mode || o.hdrSettings != normalizedHDRSettings(settings)
}
