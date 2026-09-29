package drm

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

const (
	ioctlCreateLease = 0xC01864C6
	ioctlRevokeLease = 0xC00464C9
	ioctlDropMaster  = 0x641f
)

type modeCreateLease struct {
	objects                  uint64
	count, flags, lessee, fd uint32
}
type modeRevokeLease struct{ lessee uint32 }

func (k kmsDevice) createLease(objects []uint32) (*os.File, uint32, error) {
	if len(objects) == 0 {
		return nil, 0, errors.New("empty lease")
	}
	q := modeCreateLease{objects: uint64(uintptr(unsafe.Pointer(&objects[0]))), count: uint32(len(objects)), flags: unix.O_CLOEXEC}
	err := ioctl(k.fd, ioctlCreateLease, unsafe.Pointer(&q))
	runtime.KeepAlive(objects)
	if err != nil {
		return nil, 0, err
	}
	return os.NewFile(uintptr(q.fd), "drm lease"), q.lessee, nil
}
func (k kmsDevice) revokeLease(id uint32) error {
	return ioctl(k.fd, ioctlRevokeLease, unsafe.Pointer(&modeRevokeLease{lessee: id}))
}
func (k kmsDevice) resources() ([]uint32, []uint32, error) { return resources(k.fd) }
func (k kmsDevice) connector(id uint32) (connector, error) { return readConnector(k.fd, id) }
func (k kmsDevice) pickCrtc(c connector, crtcs []uint32, used map[uint32]bool) (uint32, error) {
	return pickCrtc(k.fd, c, crtcs, used)
}

type leaseRecord struct {
	names         []string
	crtcs, planes []uint32
}

// Leasable returns connected non-desktop connectors not held by a lease.
// Scan and lease operations share one owner goroutine.
func (c *Card) Leasable() []ports.LeaseConnector {
	var result []ports.LeaseConnector
	for _, conn := range c.leasable {
		if !conn.connected || c.leasedName(conn.name) {
			continue
		}
		m := readMonitor(c.path, conn.name)
		result = append(result, ports.LeaseConnector{Card: c.path, Name: conn.name, Description: fmt.Sprintf("%s %s", m.Make, m.Model), ConnectorID: conn.id})
	}
	slices.SortFunc(result, func(a, b ports.LeaseConnector) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return result
}
func (c *Card) leasedName(name string) bool {
	for _, l := range c.leases {
		if slices.Contains(l.names, name) {
			return true
		}
	}
	return false
}
func (c *Card) Lease(names []string) (*os.File, uint32, error) {
	if len(names) == 0 {
		return nil, 0, errors.New("empty lease")
	}
	used := map[uint32]bool{}
	planesUsed := map[uint32]bool{}
	for _, o := range c.outputs {
		used[o.crtc] = true
		for _, p := range o.owned() {
			planesUsed[p.id] = true
		}
	}
	for _, l := range c.leases {
		for _, id := range l.crtcs {
			used[id] = true
		}
		for _, id := range l.planes {
			planesUsed[id] = true
		}
	}
	objects := make([]uint32, 0, len(names)*3)
	record := leaseRecord{}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || c.leasedName(name) {
			return nil, 0, fmt.Errorf("connector %s unavailable", name)
		}
		seen[name] = true
		var conn connector
		found := false
		for _, v := range c.leasable {
			if v.name == name && v.connected {
				conn = v
				found = true
				break
			}
		}
		if !found {
			return nil, 0, fmt.Errorf("connector %s unavailable", name)
		}
		crtc, err := c.k.pickCrtc(conn, c.crtcs, used)
		if err != nil {
			return nil, 0, err
		}
		pipe := slices.Index(c.crtcs, crtc)
		if pipe < 0 {
			return nil, 0, fmt.Errorf("CRTC %d not on card", crtc)
		}
		ps, err := readPlanes(c.k, pipe)
		if err != nil {
			return nil, 0, err
		}
		var planeID uint32
		for _, p := range ps {
			if p.typ == planePrimary && !planesUsed[p.id] && !c.taken[p.id] {
				planeID = p.id
				break
			}
		}
		if planeID == 0 {
			return nil, 0, fmt.Errorf("no primary plane for %s", name)
		}
		used[crtc] = true
		planesUsed[planeID] = true
		record.names = append(record.names, name)
		record.crtcs = append(record.crtcs, crtc)
		record.planes = append(record.planes, planeID)
		objects = append(objects, conn.id, crtc, planeID)
	}
	fd, id, err := c.k.createLease(objects)
	if err != nil {
		return nil, 0, err
	}
	if c.leases == nil {
		c.leases = map[uint32]leaseRecord{}
	}
	c.leases[id] = record
	for _, plane := range record.planes {
		c.taken[plane] = true
	}
	return fd, id, nil
}
func (c *Card) Revoke(id uint32) error {
	record, ok := c.leases[id]
	if !ok {
		return nil
	}
	if err := c.k.revokeLease(id); err != nil {
		return err
	}
	for _, plane := range record.planes {
		delete(c.taken, plane)
	}
	delete(c.leases, id)
	return nil
}
func (c *Card) LeaseIDs() []uint32 {
	ids := make([]uint32, 0, len(c.leases))
	for id := range c.leases {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// ClientFD opens a separate card fd and makes sure it is not a DRM master.
func (c *Card) ClientFD() (*os.File, error) {
	fd, err := unix.Open(c.path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err = ioctl(fd, ioctlDropMaster, nil); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EACCES) {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), c.path), nil
}

// FinishedLeases drains IDs revoked after a connector disappeared during Scan.
func (c *Card) FinishedLeases() []uint32 {
	ids := c.finished
	c.finished = nil
	return ids
}
