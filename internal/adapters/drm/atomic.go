package drm

import (
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
)

// Atomic KMS (ADR 015): every change on screen is one atomic commit. A
// frame commit flips the primary plane, moves the cursor plane and sets
// VRR together; the kernel waits for the frame's GPU fence (IN_FENCE_FD)
// before flipping, so no thread waits for the GPU or a vblank.

const (
	ioctlSetClientCap = 0x4010640D // DRM_IOW('d', 0x0D, struct drm_set_client_cap)
	ioctlGetPlaneRes  = 0xC01064B5 // DRM_IOWR('d', 0xB5, struct drm_mode_get_plane_res)
	ioctlGetPlane     = 0xC02064B6 // DRM_IOWR('d', 0xB6, struct drm_mode_get_plane)
	ioctlGetPropBlob  = 0xC01064AC // DRM_IOWR('d', 0xAC, struct drm_mode_get_blob)
	ioctlAtomic       = 0xC03864BC // DRM_IOWR('d', 0xBC, struct drm_mode_atomic)
	ioctlCreateBlob   = 0xC01064BD // DRM_IOWR('d', 0xBD, struct drm_mode_create_blob)
	ioctlDestroyBlob  = 0xC00464BE // DRM_IOWR('d', 0xBE, struct drm_mode_destroy_blob)

	clientCapUniversalPlanes = 2
	clientCapAtomic          = 3
	capTimestampMonotonic    = 0x6
	capAtomicAsync           = 0x15

	objPlane = 0xeeeeeeee

	flipEventFlag    = 0x1   // DRM_MODE_PAGE_FLIP_EVENT
	flipAsyncFlag    = 0x2   // DRM_MODE_PAGE_FLIP_ASYNC
	atomicTestOnly   = 0x100 // DRM_MODE_ATOMIC_TEST_ONLY
	atomicNonblock   = 0x200 // DRM_MODE_ATOMIC_NONBLOCK
	atomicAllowModes = 0x400 // DRM_MODE_ATOMIC_ALLOW_MODESET

	planeOverlay = 0
	planePrimary = 1
	planeCursor  = 2
)

type setClientCap struct{ capability, value uint64 }

type getPlaneRes struct {
	planeIDs uint64
	count, _ uint32
}

type getPlane struct {
	planeID, crtcID, fbID, possibleCrtcs, gammaSize, countFormats uint32
	formats                                                       uint64
}

type getBlob struct {
	blobID, length uint32
	data           uint64
}

type createBlob struct {
	data           uint64
	length, blobID uint32
}

type modeAtomic struct {
	flags, countObjs                                    uint32
	objs, countProps, props, values, reserved, userData uint64
}

// kms is the kernel side of the output path. kmsDevice is the real one;
// tests use its Mockery mock.
type kms interface {
	// commit issues DRM_IOCTL_MODE_ATOMIC; userData comes back in the flip event.
	commit(req *atomicReq, flags uint32, userData uint64) error
	createBlob(data []byte) (uint32, error)
	destroyBlob(id uint32) error
	getBlob(id uint32) ([]byte, error)
	// planes lists every plane with the CRTCs it can show on.
	planes() ([]planeRes, error)
	objProps(obj, typ uint32) (map[string][2]uint64, error)
	// addFB imports a single-plane dmabuf as a framebuffer of format.
	addFB(b *ports.DMABuf, format uint32) (uint32, error)
	rmFB(id uint32) error
	createLease(objects []uint32) (*os.File, uint32, error)
	revokeLease(id uint32) error
	resources() ([]uint32, []uint32, error)
	connector(id uint32) (connector, error)
	pickCrtc(c connector, crtcs []uint32, used map[uint32]bool) (uint32, error)
}

// planeRes is one plane of the card: possible is a bitmask of CRTC indices.
type planeRes struct {
	id, possible uint32
	// formats is the plane's format list (GETPLANE): used, all linear,
	// when the plane has no IN_FORMATS (drivers without modifiers).
	formats []uint32
}

// atomicReq collects (object, property, value) triples in insertion order.
type atomicReq struct {
	objs  []uint32
	props [][]propValue
}

type propValue struct {
	prop uint32
	val  uint64
}

// set adds a property; unknown properties (id 0) are skipped.
func (r *atomicReq) set(obj, prop uint32, v uint64) {
	if prop == 0 || obj == 0 {
		return
	}
	i := slices.Index(r.objs, obj)
	if i < 0 {
		r.objs = append(r.objs, obj)
		r.props = append(r.props, nil)
		i = len(r.objs) - 1
	}
	for j, p := range r.props[i] {
		if p.prop == prop {
			r.props[i][j].val = v
			return
		}
	}
	r.props[i] = append(r.props[i], propValue{prop, v})
}

// value returns a property set on an object.
func (r *atomicReq) value(obj, prop uint32) (uint64, bool) {
	i := slices.Index(r.objs, obj)
	if i < 0 {
		return 0, false
	}
	for _, p := range r.props[i] {
		if p.prop == prop {
			return p.val, true
		}
	}
	return 0, false
}

// kmsDevice drives a card fd.
type kmsDevice struct {
	fd int
	// gemMu serialises GEM handle import and close: handles are shared by
	// every import of a buffer on the fd, and outputs run on their own
	// goroutines. modifiers is DRM_CAP_ADDFB2_MODIFIERS.
	gemMu     *sync.Mutex
	modifiers bool
}

func (k kmsDevice) commit(req *atomicReq, flags uint32, userData uint64) error {
	if len(req.objs) == 0 {
		return errors.New("empty atomic commit")
	}
	counts := make([]uint32, len(req.objs))
	var props []uint32
	var vals []uint64
	for i, ps := range req.props {
		counts[i] = uint32(len(ps))
		for _, p := range ps {
			props = append(props, p.prop)
			vals = append(vals, p.val)
		}
	}
	a := modeAtomic{flags: flags, countObjs: uint32(len(req.objs)), userData: userData,
		objs: uint64(uintptr(unsafe.Pointer(&req.objs[0]))), countProps: uint64(uintptr(unsafe.Pointer(&counts[0]))),
		props: uint64(uintptr(unsafe.Pointer(&props[0]))), values: uint64(uintptr(unsafe.Pointer(&vals[0])))}
	err := ioctl(k.fd, ioctlAtomic, unsafe.Pointer(&a))
	runtime.KeepAlive(req)
	runtime.KeepAlive(counts)
	runtime.KeepAlive(props)
	runtime.KeepAlive(vals)
	return err
}

func (k kmsDevice) createBlob(data []byte) (uint32, error) {
	b := createBlob{data: uint64(uintptr(unsafe.Pointer(&data[0]))), length: uint32(len(data))}
	err := ioctl(k.fd, ioctlCreateBlob, unsafe.Pointer(&b))
	runtime.KeepAlive(data)
	return b.blobID, err
}

func (k kmsDevice) destroyBlob(id uint32) error {
	v := id
	return ioctl(k.fd, ioctlDestroyBlob, unsafe.Pointer(&v))
}

func (k kmsDevice) getBlob(id uint32) ([]byte, error) {
	b := getBlob{blobID: id}
	if err := ioctl(k.fd, ioctlGetPropBlob, unsafe.Pointer(&b)); err != nil {
		return nil, err
	}
	if b.length == 0 {
		return nil, nil
	}
	data := make([]byte, b.length)
	b.data = uint64(uintptr(unsafe.Pointer(&data[0])))
	err := ioctl(k.fd, ioctlGetPropBlob, unsafe.Pointer(&b))
	runtime.KeepAlive(data)
	return data[:min(int(b.length), len(data))], err
}

func (k kmsDevice) planes() ([]planeRes, error) {
	var r getPlaneRes
	if err := ioctl(k.fd, ioctlGetPlaneRes, unsafe.Pointer(&r)); err != nil {
		return nil, err
	}
	if r.count == 0 {
		return nil, nil
	}
	ids := make([]uint32, r.count)
	r.planeIDs = uint64(uintptr(unsafe.Pointer(&ids[0])))
	if err := ioctl(k.fd, ioctlGetPlaneRes, unsafe.Pointer(&r)); err != nil {
		return nil, err
	}
	runtime.KeepAlive(ids)
	var out []planeRes
	for _, id := range ids[:min(int(r.count), len(ids))] {
		p := getPlane{planeID: id}
		if ioctl(k.fd, ioctlGetPlane, unsafe.Pointer(&p)) != nil {
			continue
		}
		res := planeRes{id: id, possible: p.possibleCrtcs}
		if p.countFormats > 0 {
			fs := make([]uint32, p.countFormats)
			p.formats = uint64(uintptr(unsafe.Pointer(&fs[0])))
			if ioctl(k.fd, ioctlGetPlane, unsafe.Pointer(&p)) == nil {
				res.formats = fs[:min(int(p.countFormats), len(fs))]
			}
			runtime.KeepAlive(fs)
		}
		out = append(out, res)
	}
	return out, nil
}

func (k kmsDevice) objProps(obj, typ uint32) (map[string][2]uint64, error) {
	return objProps(k.fd, obj, typ)
}

func (k kmsDevice) rmFB(id uint32) error {
	v := id
	return ioctl(k.fd, ioctlRmFB, unsafe.Pointer(&v))
}

// enableAtomic turns on atomic modesetting and checks the caps outputs rely on.
func enableAtomic(fd int) error {
	for _, c := range []uint64{clientCapUniversalPlanes, clientCapAtomic} {
		v := setClientCap{capability: c, value: 1}
		if err := ioctl(fd, ioctlSetClientCap, unsafe.Pointer(&v)); err != nil {
			return err
		}
	}
	if !hasCap(fd, capTimestampMonotonic) {
		return errors.New("flip timestamps are not CLOCK_MONOTONIC")
	}
	return nil
}

func hasCap(fd int, c uint64) bool {
	v := getCap{capability: c}
	return ioctl(fd, ioctlGetCap, unsafe.Pointer(&v)) == nil && v.value == 1
}

// plane is a KMS plane an output drives, with its property IDs by name.
type plane struct {
	id, typ uint32
	props   map[string]uint32
	crtc    uint32 // CRTC_ID when read
	formats []ports.DMABufFormat
	// zposValue is the plane's zpos when read (its "zpos" property).
	zposValue uint64
}

func (p *plane) prop(name string) uint32 {
	if p == nil {
		return 0
	}
	return p.props[name]
}

// readPlanes returns the planes that can show on the CRTC at index pipe.
func readPlanes(k kms, pipe int) ([]*plane, error) {
	res, err := k.planes()
	if err != nil {
		return nil, err
	}
	var out []*plane
	for _, r := range res {
		if r.possible&(1<<pipe) == 0 {
			continue
		}
		props, err := k.objProps(r.id, objPlane)
		if err != nil {
			continue
		}
		p := &plane{id: r.id, typ: uint32(props["type"][1]), props: map[string]uint32{}, crtc: uint32(props["CRTC_ID"][1]), zposValue: props["zpos"][1]}
		for name, v := range props {
			p.props[name] = uint32(v[0])
		}
		if blob := uint32(props["IN_FORMATS"][1]); blob != 0 {
			if data, err := k.getBlob(blob); err == nil {
				p.formats = parseInFormats(data)
			}
		}
		if p.formats == nil {
			for _, f := range r.formats {
				p.formats = append(p.formats, ports.DMABufFormat{Format: f})
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// parseInFormats decodes an IN_FORMATS blob (struct drm_format_modifier_blob)
// into format and modifier pairs.
func parseInFormats(b []byte) []ports.DMABufFormat {
	if len(b) < 24 {
		return nil
	}
	le := binary.LittleEndian
	nf, fo := int(le.Uint32(b[8:])), int(le.Uint32(b[12:]))
	nm, mo := int(le.Uint32(b[16:])), int(le.Uint32(b[20:]))
	if fo < 0 || fo+nf*4 > len(b) || mo < 0 || mo+nm*24 > len(b) {
		return nil
	}
	var out []ports.DMABufFormat
	for m := range nm {
		e := b[mo+m*24:]
		mask, offset, mod := le.Uint64(e), int(le.Uint32(e[8:])), le.Uint64(e[16:])
		for bit := range 64 {
			i := offset + bit
			if mask&(1<<bit) == 0 || i >= nf {
				continue
			}
			out = append(out, ports.DMABufFormat{Format: le.Uint32(b[fo+i*4:]), Modifier: mod})
		}
	}
	return out
}

// flipEvent is a completed commit on a CRTC: when is its CLOCK_MONOTONIC
// time, seq the CRTC's vblank counter, user the commit's userData.
type flipEvent struct {
	crtc uint32
	user uint64
	when time.Duration
	seq  uint32
}

// parseFlips parses DRM events read from the card fd.
func parseFlips(buf []byte) []flipEvent {
	var out []flipEvent
	le := binary.LittleEndian
	for len(buf) >= 8 {
		typ := le.Uint32(buf)
		length := int(le.Uint32(buf[4:]))
		if length < 8 || length > len(buf) {
			break
		}
		// struct drm_event_vblank: base(8) user_data(8) sec usec sequence crtc_id.
		if typ == eventFlipDone && length >= 32 {
			sec, usec := le.Uint32(buf[16:]), le.Uint32(buf[20:])
			out = append(out, flipEvent{user: le.Uint64(buf[8:]), when: time.Duration(sec)*time.Second + time.Duration(usec)*time.Microsecond, seq: le.Uint32(buf[24:]), crtc: le.Uint32(buf[28:])})
		}
		buf = buf[length:]
	}
	return out
}
