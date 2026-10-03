package drm

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// KMS object properties, read by name; atomic commits set them.

const (
	ioctlObjGetProps = 0xC02064B9 // DRM_IOWR('d', 0xB9, struct drm_mode_obj_get_properties)
	ioctlGetProp     = 0xC04064AA // DRM_IOWR('d', 0xAA, struct drm_mode_get_property)

	objCrtc      = 0xcccccccc
	objConnector = 0xc0c0c0c0
	objColorop   = 0xfafafafa // DRM_MODE_OBJECT_COLOROP
)

type objGetProps struct {
	props, values uint64
	count, objID  uint32
	objType, _    uint32
}

type propEnum struct {
	value uint64
	name  [32]byte
}

type getProp struct {
	values, enumBlobs    uint64
	propID, flags        uint32
	name                 [32]byte
	countValues, countEn uint32
}

// objProps returns an object's property IDs and values by name.
func objProps(fd int, obj, typ uint32) (map[string][2]uint64, error) {
	q := objGetProps{objID: obj, objType: typ}
	if err := ioctl(fd, ioctlObjGetProps, unsafe.Pointer(&q)); err != nil {
		return nil, err
	}
	out := map[string][2]uint64{}
	if q.count == 0 {
		return out, nil
	}
	ids := make([]uint32, q.count)
	vals := make([]uint64, q.count)
	q.props, q.values = uint64(uintptr(unsafe.Pointer(&ids[0]))), uint64(uintptr(unsafe.Pointer(&vals[0])))
	if err := ioctl(fd, ioctlObjGetProps, unsafe.Pointer(&q)); err != nil {
		return nil, err
	}
	for i := range min(int(q.count), len(ids)) {
		p := getProp{propID: ids[i]}
		if ioctl(fd, ioctlGetProp, unsafe.Pointer(&p)) != nil {
			continue
		}
		out[unix.ByteSliceToString(p.name[:])] = [2]uint64{uint64(ids[i]), vals[i]}
	}
	return out, nil
}

// propEnums returns the enum names of property id with their values.
func propEnums(fd int, id uint32) (map[string]uint64, error) {
	p := getProp{propID: id}
	if err := ioctl(fd, ioctlGetProp, unsafe.Pointer(&p)); err != nil {
		return nil, err
	}
	if p.countEn == 0 {
		return nil, nil
	}
	enums := make([]propEnum, p.countEn)
	// The kernel also copies an enum's values when countValues is set: a
	// nil values pointer makes the whole call fail (EFAULT).
	v := make([]uint64, max(p.countValues, 1))
	p.enumBlobs = uint64(uintptr(unsafe.Pointer(&enums[0])))
	p.values = uint64(uintptr(unsafe.Pointer(&v[0])))
	if err := ioctl(fd, ioctlGetProp, unsafe.Pointer(&p)); err != nil {
		return nil, err
	}
	out := make(map[string]uint64, p.countEn)
	for _, e := range enums[:min(int(p.countEn), len(enums))] {
		out[unix.ByteSliceToString(e.name[:])] = e.value
	}
	runtime.KeepAlive(enums)
	runtime.KeepAlive(v)
	return out, nil
}

// readContentTypeProp records the connector's content type enum values.
func readContentTypeProp(fd int, props map[string][2]uint64) (uint32, [5]uint64) {
	id := uint32(props["content type"][0])
	var values [5]uint64
	if id == 0 {
		return 0, values
	}
	enums, err := propEnums(fd, id)
	if err != nil || len(enums) == 0 {
		return 0, values
	}
	for i, name := range [...]string{"No Data", "Graphics", "Photo", "Cinema", "Game"} {
		values[i] = enums[name]
	}
	return id, values
}

// connectorHDRProps retains connector property IDs, enum values and the
// original max bpc for SDR modesets and restoration.
type connectorHDRProps struct {
	Metadata, Colorspace, MaxBPC uint32
	BT2020Value                  uint64
	DefaultValue                 uint64 // connector Colorspace "Default" enum
	HasDefault                   bool
	MaxBPCValue                  uint64 // current value for eventual restoration
}

// readConnectorHDRProps checks property metadata, not only current values.
func readConnectorHDRProps(fd int, props map[string][2]uint64) connectorHDRProps {
	var out connectorHDRProps
	out.Metadata = uint32(props["HDR_OUTPUT_METADATA"][0])
	if id := uint32(props["Colorspace"][0]); id != 0 {
		p := getProp{propID: id}
		if ioctl(fd, ioctlGetProp, unsafe.Pointer(&p)) == nil && p.countEn > 0 {
			enums := make([]propEnum, p.countEn)
			p.enumBlobs = uint64(uintptr(unsafe.Pointer(&enums[0])))
			// The kernel also copies an enum's values when countValues is
			// set: a nil values pointer makes the whole call fail (EFAULT).
			values := make([]uint64, max(p.countValues, 1))
			p.values = uint64(uintptr(unsafe.Pointer(&values[0])))
			if ioctl(fd, ioctlGetProp, unsafe.Pointer(&p)) == nil {
				out.Colorspace, out.BT2020Value = bt2020Enum(id, enums[:min(int(p.countEn), len(enums))])
				for _, e := range enums[:min(int(p.countEn), len(enums))] {
					if unix.ByteSliceToString(e.name[:]) == "Default" {
						out.DefaultValue, out.HasDefault = e.value, true
					}
				}
			}
			runtime.KeepAlive(enums)
			runtime.KeepAlive(values)
		}
	}
	if id := uint32(props["max bpc"][0]); id != 0 {
		p := getProp{propID: id}
		if ioctl(fd, ioctlGetProp, unsafe.Pointer(&p)) == nil && p.countValues >= 2 {
			values := make([]uint64, p.countValues)
			p.values = uint64(uintptr(unsafe.Pointer(&values[0])))
			if ioctl(fd, ioctlGetProp, unsafe.Pointer(&p)) == nil && p.countValues >= 2 && bpcAllows10(values) {
				out.MaxBPC, out.MaxBPCValue = id, props["max bpc"][1]
			}
			runtime.KeepAlive(values)
		}
	}
	return out
}

func bt2020Enum(id uint32, enums []propEnum) (uint32, uint64) {
	for _, e := range enums {
		if unix.ByteSliceToString(e.name[:]) == "BT2020_RGB" {
			return id, e.value
		}
	}
	return 0, 0
}

func bpcAllows10(values []uint64) bool {
	return len(values) >= 2 && values[0] <= 10 && values[1] >= 10
}

// hdrCapability combines EDID and connector support, retaining the first reason.
type hdrCapability struct {
	Capable bool
	Reason  string
	HDRMetadata
}

func detectHDR(m Monitor, p connectorHDRProps) hdrCapability {
	c := hdrCapability{HDRMetadata: m.HDR}
	switch {
	case !m.HDR.PQ:
		c.Reason = "edid: no PQ"
	case !m.HDR.StaticType1:
		c.Reason = "edid: no static metadata type 1"
	case !m.HDR.BT2020RGB:
		c.Reason = "edid: no BT2020RGB"
	case p.Metadata == 0:
		c.Reason = "connector: no HDR_OUTPUT_METADATA"
	case p.Colorspace == 0:
		c.Reason = "connector: no BT2020_RGB Colorspace"
	case p.MaxBPC == 0:
		c.Reason = "connector: max bpc below 10"
	case !p.HasDefault:
		c.Reason = "connector: no Default Colorspace"
	default:
		c.Capable = true
	}
	return c
}

// vrrProperty returns the CRTC's VRR_ENABLED property when the connector
// is VRR capable, else 0.
func vrrProperty(k kms, conn, crtc uint32) uint32 {
	cp, err := k.objProps(conn, objConnector)
	if err != nil || cp["vrr_capable"][1] != 1 {
		return 0
	}
	rp, err := k.objProps(crtc, objCrtc)
	if err != nil {
		return 0
	}
	return uint32(rp["VRR_ENABLED"][0])
}
