package vulkan

import (
	"errors"
	"image"
	"testing"

	vk "bnema.dev/purego-vulkan/vulkan"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// syncPipe stands in for an exported sync file: its read end is handed out,
// and closing it shows on the write end (EPIPE).
func syncPipe(t *testing.T) (read int32, closed func() bool) {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(p[1]) })
	return int32(p[0]), func() bool {
		_, err := unix.Write(p[1], []byte{1})
		return errors.Is(err, unix.EPIPE)
	}
}

// castSyncRenderer renders a frame and returns a lease and a linear cast
// buffer to copy it into, with the fence seam mocked.
func castSyncRenderer(t *testing.T) (*Renderer, *captureFrame, domain.DMABuf, *mockdmabufFences) {
	t.Helper()
	r := newTestRenderer(t, 16, 8)
	requireCaptureSync(t, r)
	if err := render(r, domain.Scene{Background: "#0000ff"}, nil); err != nil {
		t.Fatal(err)
	}
	buf := castLinear(t, r.gpu, fourcc('X', 'R', '2', '4'), 16, 8)
	cf := captureWait(t, r).(*captureFrame)
	t.Cleanup(func() { r.EndCapture(cf) })
	m := newMockdmabufFences(t)
	r.dmaFences = m
	return r, cf, buf, m
}

// Every failure to establish the wait on the buffer's fences fails CopyTo
// before anything is submitted, and closes the exported sync file.
func TestCastCopyFailsClosedWithoutSync(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name  string
		setup func(m *mockdmabufFences, fd int32)
		fdOut bool // a sync file was exported and must be closed
	}{
		{"export", func(m *mockdmabufFences, _ int32) {
			m.EXPECT().exportSyncFile(mock.Anything).Return(-1, boom).Once()
		}, false},
		{"create", func(m *mockdmabufFences, fd int32) {
			m.EXPECT().exportSyncFile(mock.Anything).Return(fd, nil).Once()
			m.EXPECT().createSemaphore().Return(vk.Semaphore(0), boom).Once()
		}, true},
		{"import", func(m *mockdmabufFences, fd int32) {
			m.EXPECT().exportSyncFile(mock.Anything).Return(fd, nil).Once()
			m.EXPECT().createSemaphore().Return(vk.Semaphore(77), nil).Once()
			m.EXPECT().importSyncFD(vk.Semaphore(77), fd).Return(vk.ErrorOutOfHostMemory).Once()
			m.EXPECT().destroySemaphore(vk.Semaphore(77)).Return().Once()
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cf, buf, m := castSyncRenderer(t)
			fd, closed := syncPipe(t)
			tc.setup(m, fd)
			before := len(cf.slot.copies)
			done, err := cf.CopyTo(image.Rect(0, 0, 16, 8), buf)
			if err == nil || done != nil {
				t.Fatalf("CopyTo without sync: %v %v", done, err)
			}
			if tc.name != "import" && !errors.Is(err, boom) {
				t.Fatalf("error %v", err)
			}
			if len(cf.slot.copies) != before {
				t.Fatal("copy submitted without sync")
			}
			if tc.fdOut && !closed() {
				t.Fatal("exported sync file leaked")
			}
			if !tc.fdOut {
				_ = unix.Close(int(fd))
			}
		})
	}
}

// No fence pending is not a failure: the copy is submitted with no wait.
func TestCastCopyNoPendingFence(t *testing.T) {
	_, cf, buf, m := castSyncRenderer(t)
	m.EXPECT().exportSyncFile(mock.Anything).Return(-1, nil).Once()
	castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
}

// A real exported fence is waited on and its semaphore destroyed with the copy.
func TestCastCopyWaitsOnExportedFence(t *testing.T) {
	r, cf, buf, m := castSyncRenderer(t)
	dev := deviceFences{r}
	m.EXPECT().exportSyncFile(mock.Anything).RunAndReturn(dev.exportSyncFile).Twice()
	m.EXPECT().createSemaphore().RunAndReturn(dev.createSemaphore).Maybe()
	m.EXPECT().importSyncFD(mock.Anything, mock.Anything).RunAndReturn(dev.importSyncFD).Maybe()
	m.EXPECT().destroySemaphore(mock.Anything).Run(dev.destroySemaphore).Maybe()
	castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
	castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
}
