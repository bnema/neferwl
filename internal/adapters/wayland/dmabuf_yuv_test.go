package wayland

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/linuxdmabuf"
	"golang.org/x/sys/unix"
)

func TestYUVParams(t *testing.T) {
	for _, tc := range []struct {
		name        string
		format      uint32
		two         bool
		modMismatch bool
		short       bool
		errCode     uint32
	}{
		{name: "NV12", format: fourccNV12, two: true},
		{name: "P010", format: fourccP010, two: true},
		{name: "missing plane", format: fourccNV12, errCode: uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorIncomplete)},
		{name: "extra plane", format: fourccNV12, two: true, errCode: uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorIncomplete)},
		{name: "modifier mismatch", format: fourccNV12, two: true, modMismatch: true, errCode: uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorInvalidFormat)},
		{name: "P010 short chroma", format: fourccP010, two: true, short: true, errCode: uint32(linuxdmabuf.ZwpLinuxBufferParamsV1ErrorOutOfBounds)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, dir := dmabufServer(t, ports.DMABufSupport{Formats: []ports.DMABufFormat{{Format: fourccNV12}, {Format: fourccP010}}})
			c := protocolClient(t, s, dir)
			dm := bindVersion(t, c, "zwp_linux_dmabuf_v1", 4)
			p := c.AllocateID()
			requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, p)
			proxy := &paramsProxy{result: make(chan uint16, 2)}
			proxy.SetID(p)
			registerWireProxy(c, proxy)
			fd, err := unix.MemfdCreate("yuv-params", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if err := unix.Ftruncate(fd, 8192); err != nil {
				t.Fatal(err)
			}
			add := func(idx, offset, stride, mod uint32) {
				t.Helper()
				if err := wireRequest(c, p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, idx, offset, stride, uint32(0), mod); err != nil {
					t.Fatal(err)
				}
			}
			stride := uint32(64)
			if tc.format == fourccP010 {
				stride = 128
			}
			add(0, 0, stride, 0)
			if tc.two {
				mod := uint32(0)
				if tc.modMismatch {
					mod = 1
				}
				offset := uint32(1024)
				if tc.short {
					offset = 8180
				}
				add(1, offset, stride, mod)
			}
			if tc.name == "extra plane" {
				add(2, 4096, 64, 0)
			}
			if tc.modMismatch {
				expectProtocolError(t, c, p, tc.errCode)
				return
			}
			requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreate, int32(64), int32(16), tc.format, uint32(0))
			if tc.errCode != 0 {
				expectProtocolError(t, c, p, tc.errCode)
				return
			}
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if op := <-proxy.result; op != uint16(linuxdmabuf.ZwpLinuxBufferParamsV1EventCreated) {
				t.Fatalf("created event %d", op)
			}
		})
	}
}
