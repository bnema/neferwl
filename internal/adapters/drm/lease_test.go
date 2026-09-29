package drm

import (
	"context"
	"os"
	"slices"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/stretchr/testify/mock"
)

func TestLeaseResourceOwnership(t *testing.T) {
	k := newMockkms(t)
	conn := connector{id: 40, name: "DP-1", connected: true, nonDesktop: true, encoders: []uint32{3}}
	c := &Card{path: "/dev/dri/card-test", crtcs: []uint32{30}, outputs: map[string]*Output{}, leasable: map[string]connector{conn.name: conn}, leases: map[uint32]leaseRecord{}, taken: map[uint32]bool{}, k: k}
	k.EXPECT().pickCrtc(conn, []uint32{30}, mock.Anything).RunAndReturn(func(_ connector, _ []uint32, used map[uint32]bool) (uint32, error) {
		if used[30] {
			t.Fatal("CRTC already used")
		}
		return 30, nil
	})
	k.EXPECT().planes().Return([]planeRes{{id: 50, possible: 1}}, nil)
	k.EXPECT().objProps(uint32(50), uint32(objPlane)).Return(map[string][2]uint64{"type": {1, planePrimary}}, nil)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	k.EXPECT().createLease(mock.Anything).RunAndReturn(func(ids []uint32) (*os.File, uint32, error) {
		if !slices.Equal(ids, []uint32{40, 30, 50}) {
			t.Errorf("objects %v", ids)
		}
		return w, 7, nil
	})
	f, id, err := c.Lease([]string{"DP-1"})
	if err != nil || f != w || id != 7 {
		t.Fatalf("lease fd %v id %d err %v", f, id, err)
	}
	if len(c.Leasable()) != 0 {
		t.Fatal("leased connector advertised")
	}
	if _, _, err = c.Lease([]string{"DP-1"}); err == nil {
		t.Fatal("duplicate lease allowed")
	}
	k.EXPECT().revokeLease(uint32(7)).Return(nil)
	if err = c.Revoke(7); err != nil {
		t.Fatal(err)
	}
	if len(c.Leasable()) != 1 {
		t.Fatal("connector not available after revoke")
	}
}

func TestLeaseABI(t *testing.T) {
	if unsafe.Sizeof(modeCreateLease{}) != 24 || unsafe.Sizeof(modeRevokeLease{}) != 4 {
		t.Fatal("DRM lease ioctl structs")
	}
	if ioctlCreateLease>>16&0x3fff != uint64(unsafe.Sizeof(modeCreateLease{})) || ioctlRevokeLease>>16&0x3fff != uint64(unsafe.Sizeof(modeRevokeLease{})) {
		t.Fatal("DRM lease ioctl sizes")
	}
}

func TestScanNonDesktopAndRevokeDisconnected(t *testing.T) {
	k := newMockkms(t)
	conn := connector{id: 40, name: "DP-1", connected: true, nonDesktop: true, modes: []modeInfo{{HDisplay: 1920}}, encoders: []uint32{3}}
	c := &Card{k: k, path: "/dev/dri/card-test", crtcs: []uint32{30}, outputs: map[string]*Output{}, leases: map[uint32]leaseRecord{7: {names: []string{"DP-1"}, crtcs: []uint32{30}, planes: []uint32{50}}}, want: Want{}, log: logging.For(context.Background(), "drm"), taken: map[uint32]bool{}}
	k.EXPECT().resources().Return([]uint32{30}, []uint32{40}, nil).Once()
	k.EXPECT().connector(uint32(40)).Return(conn, nil).Once()
	added, _, _, err := c.Scan()
	if err != nil || len(added) != 0 {
		t.Fatalf("non-desktop output: %v %v", added, err)
	}
	k.EXPECT().resources().Return([]uint32{30}, []uint32{40}, nil).Once()
	conn.connected = false
	k.EXPECT().connector(uint32(40)).Return(conn, nil).Once()
	k.EXPECT().revokeLease(uint32(7)).Return(nil)
	_, _, _, err = c.Scan()
	if err != nil || !slices.Equal(c.FinishedLeases(), []uint32{7}) {
		t.Fatalf("hotplug revoke: %v", err)
	}
}
