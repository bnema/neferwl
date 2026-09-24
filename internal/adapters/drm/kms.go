// Package drm drives one KMS output with raw ioctls and dumb buffers.
package drm

import (
	"errors"
	"fmt"
	"math"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioctl numbers for amd64/arm64 (DRM_IOWR('d', nr, size)).
const (
	ioctlGetResources = 0xC04064A0
	ioctlGetCrtc      = 0xC06864A1
	ioctlSetCrtc      = 0xC06864A2
	ioctlGetEncoder   = 0xC01464A6
	ioctlGetConnector = 0xC05064A7
	ioctlAddFB        = 0xC01C64AE
	ioctlRmFB         = 0xC00464AF
	ioctlPageFlip     = 0xC01864B0
	ioctlCreateDumb   = 0xC02064B2
	ioctlMapDumb      = 0xC01064B3
	ioctlDestroyDumb  = 0xC00464B4

	pageFlipEvent    = 1
	eventFlipDone    = 2
	modeTypePrefered = 1 << 3
	connected        = 1
)

type cardRes struct {
	fbs, crtcs, connectors, encoders                uint64
	countFbs, countCrtcs, countConns, countEncoders uint32
	minW, maxW, minH, maxH                          uint32
}

type modeInfo struct {
	Clock                                  uint32
	HDisplay, HSyncStart, HSyncEnd, HTotal uint16
	HSkew                                  uint16
	VDisplay, VSyncStart, VSyncEnd, VTotal uint16
	VScan                                  uint16
	VRefresh, Flags, Type                  uint32
	Name                                   [32]byte
}

func (m modeInfo) String() string {
	return fmt.Sprintf("%dx%d@%.3f", m.HDisplay, m.VDisplay, float64(m.refreshMilli())/1000)
}

type getConnector struct {
	encoders, modes, props, propValues       uint64
	countModes, countProps, countEncoders    uint32
	encoderID, connectorID, connType, typeID uint32
	connection, mmW, mmH, subpixel           uint32
	_                                        uint32
}

type getEncoder struct{ encoderID, encoderType, crtcID, possibleCrtcs, possibleClones uint32 }

type modeCrtc struct {
	// unsafe.Pointer (not uint64) so the GC keeps it valid if the stack moves.
	setConnectors   unsafe.Pointer
	countConnectors uint32
	crtcID, fbID    uint32
	x, y            uint32
	gammaSize       uint32
	modeValid       uint32
	mode            modeInfo
}

type pageFlip struct {
	crtcID, fbID, flags, reserved uint32
	userData                      uint64
}

type createDumb struct {
	height, width, bpp, flags uint32
	handle, pitch             uint32
	size                      uint64
}

type mapDumb struct {
	handle, pad uint32
	offset      uint64
}

type fbCmd struct{ fbID, width, height, pitch, bpp, depth, handle uint32 }

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		if e == unix.EINTR || e == unix.EAGAIN {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

// connector is the subset of KMS connector state used to pick an output.
type connector struct {
	id, encoderID uint32
	name          string
	connected     bool
	mmW, mmH      int
	modes         []modeInfo
	encoders      []uint32
}

var connectorTypes = map[uint32]string{1: "VGA", 2: "DVI-I", 3: "DVI-D", 10: "DP", 11: "HDMI-A", 12: "HDMI-B", 7: "LVDS", 14: "eDP", 15: "Virtual", 16: "DSI", 17: "DPI", 20: "USB"}

// Want selects a connector and mode; see ports.OutputConfig.
type Want struct {
	Name     string
	W, H     int
	Hz       float64 // 0: highest refresh at W x H
	Disabled map[string]bool
}

// refreshMilli is the exact refresh in mHz, computed from the timings like wlroots.
func (m modeInfo) refreshMilli() int {
	if m.HTotal == 0 || m.VTotal == 0 {
		return int(m.VRefresh) * 1000
	}
	r := (int64(m.Clock)*1_000_000/int64(m.HTotal) + int64(m.VTotal)/2) / int64(m.VTotal)
	if m.Flags&(1<<4) != 0 { // interlace
		r *= 2
	}
	if m.Flags&(1<<5) != 0 { // doublescan
		r /= 2
	}
	if m.VScan > 1 {
		r /= int64(m.VScan)
	}
	return int(r)
}

// pickConnector applies want: the named connector if connected, else the first
// connected one that is not disabled. The mode is the requested one, else preferred.
func pickConnector(conns []connector, want Want) (connector, modeInfo, error) {
	var chosen *connector
	for i := range conns {
		c := &conns[i]
		if !c.connected || len(c.modes) == 0 || want.Disabled[c.name] {
			continue
		}
		if c.name == want.Name {
			chosen = c
			break
		}
		if chosen == nil {
			chosen = c
		}
	}
	if chosen == nil {
		return connector{}, modeInfo{}, errors.New("no connected display")
	}
	if chosen.name == want.Name && want.W > 0 {
		if m, ok := pickMode(chosen.modes, want.W, want.H, want.Hz); ok {
			return *chosen, m, nil
		}
	}
	for _, m := range chosen.modes {
		if m.Type&modeTypePrefered != 0 {
			return *chosen, m, nil
		}
	}
	return *chosen, chosen.modes[0], nil
}

// pickMode finds W x H with the refresh closest to hz, or the highest when hz is 0.
func pickMode(modes []modeInfo, w, h int, hz float64) (modeInfo, bool) {
	best, found := modeInfo{}, false
	score := func(m modeInfo) float64 {
		r := float64(m.refreshMilli()) / 1000
		if hz == 0 {
			return -r
		}
		return math.Abs(r - hz)
	}
	for _, m := range modes {
		if int(m.HDisplay) != w || int(m.VDisplay) != h || m.Flags&(1<<4) != 0 {
			continue
		}
		if !found || score(m) < score(best) {
			best, found = m, true
		}
	}
	return best, found
}

func resources(fd int) (crtcs, conns []uint32, err error) {
	var r cardRes
	if err := ioctl(fd, ioctlGetResources, unsafe.Pointer(&r)); err != nil {
		return nil, nil, fmt.Errorf("get resources: %w", err)
	}
	crtcs = make([]uint32, r.countCrtcs)
	conns = make([]uint32, r.countConns)
	r = cardRes{countCrtcs: r.countCrtcs, countConns: r.countConns}
	if len(crtcs) > 0 {
		r.crtcs = uint64(uintptr(unsafe.Pointer(&crtcs[0])))
	}
	if len(conns) > 0 {
		r.connectors = uint64(uintptr(unsafe.Pointer(&conns[0])))
	}
	if err := ioctl(fd, ioctlGetResources, unsafe.Pointer(&r)); err != nil {
		return nil, nil, fmt.Errorf("get resources: %w", err)
	}
	return crtcs[:min(int(r.countCrtcs), len(crtcs))], conns[:min(int(r.countConns), len(conns))], nil
}

func readConnector(fd int, id uint32) (connector, error) {
	g := getConnector{connectorID: id}
	if err := ioctl(fd, ioctlGetConnector, unsafe.Pointer(&g)); err != nil {
		return connector{}, err
	}
	modes := make([]modeInfo, g.countModes)
	encs := make([]uint32, g.countEncoders)
	g2 := getConnector{connectorID: id, countModes: g.countModes, countEncoders: g.countEncoders}
	if len(modes) > 0 {
		g2.modes = uint64(uintptr(unsafe.Pointer(&modes[0])))
	}
	if len(encs) > 0 {
		g2.encoders = uint64(uintptr(unsafe.Pointer(&encs[0])))
	}
	if err := ioctl(fd, ioctlGetConnector, unsafe.Pointer(&g2)); err != nil {
		return connector{}, err
	}
	name := connectorTypes[g2.connType]
	if name == "" {
		name = "Unknown"
	}
	return connector{
		id: id, encoderID: g2.encoderID, name: fmt.Sprintf("%s-%d", name, g2.typeID),
		connected: g2.connection == connected, mmW: int(g2.mmW), mmH: int(g2.mmH),
		modes:    modes[:min(int(g2.countModes), len(modes))],
		encoders: encs[:min(int(g2.countEncoders), len(encs))],
	}, nil
}

// pickCrtc prefers the connector's current encoder CRTC, else any CRTC an encoder allows.
func pickCrtc(fd int, c connector, crtcs []uint32) (uint32, error) {
	encs := c.encoders
	if c.encoderID != 0 {
		encs = append([]uint32{c.encoderID}, encs...)
	}
	for _, id := range encs {
		e := getEncoder{encoderID: id}
		if ioctl(fd, ioctlGetEncoder, unsafe.Pointer(&e)) != nil {
			continue
		}
		if id == c.encoderID && e.crtcID != 0 {
			return e.crtcID, nil
		}
		for i, crtc := range crtcs {
			if e.possibleCrtcs&(1<<i) != 0 {
				return crtc, nil
			}
		}
	}
	return 0, fmt.Errorf("no CRTC for %s", c.name)
}

type dumbBuffer struct {
	handle, fbID, pitch uint32
	mem                 []byte
}

func newDumb(fd int, w, h int) (*dumbBuffer, error) {
	c := createDumb{width: uint32(w), height: uint32(h), bpp: 32}
	if err := ioctl(fd, ioctlCreateDumb, unsafe.Pointer(&c)); err != nil {
		return nil, fmt.Errorf("create dumb: %w", err)
	}
	b := &dumbBuffer{handle: c.handle, pitch: c.pitch}
	f := fbCmd{width: uint32(w), height: uint32(h), pitch: c.pitch, bpp: 32, depth: 24, handle: c.handle}
	if err := ioctl(fd, ioctlAddFB, unsafe.Pointer(&f)); err != nil {
		b.destroy(fd)
		return nil, fmt.Errorf("add fb: %w", err)
	}
	b.fbID = f.fbID
	m := mapDumb{handle: c.handle}
	if err := ioctl(fd, ioctlMapDumb, unsafe.Pointer(&m)); err != nil {
		b.destroy(fd)
		return nil, fmt.Errorf("map dumb: %w", err)
	}
	mem, err := unix.Mmap(fd, int64(m.offset), int(c.size), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		b.destroy(fd)
		return nil, fmt.Errorf("mmap dumb: %w", err)
	}
	b.mem = mem
	return b, nil
}

func (b *dumbBuffer) destroy(fd int) {
	if b.mem != nil {
		_ = unix.Munmap(b.mem)
		b.mem = nil
	}
	if b.fbID != 0 {
		id := b.fbID
		_ = ioctl(fd, ioctlRmFB, unsafe.Pointer(&id))
		b.fbID = 0
	}
	h := b.handle
	_ = ioctl(fd, ioctlDestroyDumb, unsafe.Pointer(&h))
}

func setCrtc(fd int, crtc, conn, fb uint32, mode *modeInfo) error {
	ids := []uint32{conn}
	c := modeCrtc{crtcID: crtc, fbID: fb, countConnectors: 1, setConnectors: unsafe.Pointer(&ids[0])}
	if mode != nil {
		c.mode, c.modeValid = *mode, 1
	}
	return ioctl(fd, ioctlSetCrtc, unsafe.Pointer(&c))
}

func getCrtc(fd int, crtc uint32) (modeCrtc, error) {
	c := modeCrtc{crtcID: crtc}
	err := ioctl(fd, ioctlGetCrtc, unsafe.Pointer(&c))
	return c, err
}

func flip(fd int, crtc, fb uint32) error {
	p := pageFlip{crtcID: crtc, fbID: fb, flags: pageFlipEvent}
	return ioctl(fd, ioctlPageFlip, unsafe.Pointer(&p))
}

// countFlipEvents parses DRM events read from the card fd.
func countFlipEvents(buf []byte) int {
	n := 0
	for len(buf) >= 8 {
		typ := uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24
		length := int(uint32(buf[4]) | uint32(buf[5])<<8 | uint32(buf[6])<<16 | uint32(buf[7])<<24)
		if length < 8 || length > len(buf) {
			break
		}
		if typ == eventFlipDone {
			n++
		}
		buf = buf[length:]
	}
	return n
}
