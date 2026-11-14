package vulkan

import (
	"errors"
	"os"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCaptureSyncExportProbeGatesFormats(t *testing.T) {
	g := newTestGPU(t)
	// Obtain the ungated baseline once; the probe must not alter modifiers.
	g.captureSyncExport = true
	capture := g.CaptureFormats()
	cast := g.CastFormats()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unsupported", unix.ENOTTY}, {"failed", unix.EIO}, {"supported", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newMockdmabufFences(t)
			var buffer *os.File
			fs.EXPECT().exportSyncFile(buffer).Return(int32(-1), tc.err).Once()
			g.checkCaptureSyncExport(fs, buffer)
			if tc.err != nil {
				if g.captureSyncExport || len(g.CaptureFormats()) != 0 || len(g.CastFormats()) != 0 {
					t.Fatal("unsupported sync export advertised DMA-BUF formats")
				}
			} else if !g.captureSyncExport || !slices.Equal(capture, g.CaptureFormats()) || !slices.Equal(cast, g.CastFormats()) {
				t.Fatal("supported sync export changed formats")
			}
		})
	}
}

func TestCaptureSyncExportProbeClosesFence(t *testing.T) {
	g := &GPU{}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(p[1]) }()
	fs := newMockdmabufFences(t)
	var buffer *os.File
	fs.EXPECT().exportSyncFile(buffer).Return(int32(p[0]), nil).Once()
	g.checkCaptureSyncExport(fs, buffer)
	var st unix.Stat_t
	if err := unix.Fstat(p[0], &st); !errors.Is(err, unix.EBADF) {
		t.Fatalf("probe fence not closed: %v", err)
	}
}
