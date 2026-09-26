package drm

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// KMS object properties, read by name; atomic commits set them.

const (
	ioctlObjGetProps = 0xC02064B9 // DRM_IOWR('d', 0xB9, struct drm_mode_obj_get_properties)
	ioctlGetProp     = 0xC04064AA // DRM_IOWR('d', 0xAA, struct drm_mode_get_property)

	objCrtc      = 0xcccccccc
	objConnector = 0xc0c0c0c0
)

type objGetProps struct {
	props, values uint64
	count, objID  uint32
	objType, _    uint32
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
