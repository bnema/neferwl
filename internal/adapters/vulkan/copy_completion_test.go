package vulkan

import (
	"errors"
	"fmt"
	"testing"
	"time"

	vk "bnema.dev/purego-vulkan/vulkan"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestFailedExportCompletion(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		wait, queue, device         vk.Result
		wantQueue, wantDevice, lost bool
	}{
		{name: "fence complete", wait: vk.Success},
		{name: "fence timeout", wait: vk.Timeout, lost: true},
		{name: "fence device lost", wait: vk.ErrorDeviceLost, lost: true},
		{name: "queue completes", wait: vk.ErrorOutOfHostMemory, queue: vk.Success, wantQueue: true},
		{name: "queue device lost", wait: vk.ErrorOutOfHostMemory, queue: vk.ErrorDeviceLost, wantQueue: true, lost: true},
		{name: "device completes", wait: vk.ErrorOutOfHostMemory, queue: vk.ErrorOutOfDeviceMemory, device: vk.Success, wantQueue: true, wantDevice: true},
		{name: "device lost", wait: vk.ErrorOutOfHostMemory, queue: vk.ErrorOutOfDeviceMemory, device: vk.ErrorDeviceLost, wantQueue: true, wantDevice: true, lost: true},
		{name: "completion unproven", wait: vk.ErrorOutOfHostMemory, queue: vk.ErrorOutOfDeviceMemory, device: vk.ErrorOutOfHostMemory, wantQueue: true, wantDevice: true, lost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sync := newMockcopyCompletion(t)
			calls := []*mock.Call{sync.EXPECT().waitCopy(vk.Fence(42)).Return(tc.wait).Once()}
			if tc.wantQueue {
				calls = append(calls, sync.EXPECT().queueIdle().Return(tc.queue).Once())
			}
			if tc.wantDevice {
				calls = append(calls, sync.EXPECT().deviceIdle().Return(tc.device).Once())
			}
			mock.InOrder(calls...)
			exportErr := errors.New("semaphore export failed")
			err := errors.Join(exportErr, finishFailedExport(sync, vk.Fence(42)))
			require.ErrorIs(t, err, exportErr)
			require.Equal(t, tc.lost, errors.Is(err, ports.ErrDeviceLost))
		})
	}
}

func TestFailedExportTimeoutMarksLost(t *testing.T) {
	sync := newMockcopyCompletion(t)
	sync.EXPECT().waitCopy(vk.Fence(42)).Return(vk.Timeout).Once()
	r := &Renderer{gpu: &GPU{}}
	c := &dmaCopy{fence: vk.Fence(42)}
	slot := &captureSlot{copies: []*dmaCopy{c}, leased: true}
	frame := &captureFrame{r: r, slot: slot, valid: true}
	err := r.finishFailedExport(sync, c.fence)
	require.ErrorIs(t, err, ports.ErrDeviceLost)
	require.True(t, r.lost)
	require.True(t, r.gpu.lost)
	r.EndCapture(frame)
	r.freeCaptureSlot(slot)
	r.destroyCaptures()
	require.True(t, frame.valid)
	require.True(t, slot.leased)
	require.Equal(t, []*dmaCopy{c}, slot.copies)
}

func TestLostRendererEntryPointsSkipDispatch(t *testing.T) {
	r := &Renderer{lost: true}
	_, err := r.Render(domain.Scene{}, nil)
	require.ErrorIs(t, err, ports.ErrDeviceLost)
	err = r.oneShot(func(vk.CommandBuffer) { t.Fatal("record called after loss") })
	require.ErrorIs(t, err, ports.ErrDeviceLost)
	_, err = r.beginCapture(true)
	require.ErrorIs(t, err, ports.ErrDeviceLost)
}

func TestLostDropTargetsSkipsFree(t *testing.T) {
	tg := &target{image: vk.Image(1)}
	r := &Renderer{lost: true, gpu: &GPU{}, targets: []*target{tg}, last: tg, current: 1}
	r.dropTargets()
	require.Empty(t, r.targets)
	require.Nil(t, r.last)
	require.Zero(t, r.current)
	require.Equal(t, vk.Image(1), tg.image)
}

func TestLostTargetAndTrimEntries(t *testing.T) {
	r := &Renderer{lost: true, targets: []*target{{image: 1}}, deferred: []deferredFree{{frame: 0, sem: 1}}}
	_, err := r.ExportTargets(1, nil, false)
	require.ErrorIs(t, err, ports.ErrDeviceLost)
	require.ErrorIs(t, r.ImportTargets(nil), ports.ErrDeviceLost)
	require.ErrorIs(t, r.Trim(time.Time{}), ports.ErrDeviceLost)
	require.ErrorIs(t, r.poll(), ports.ErrDeviceLost)
	r.runDeferred()
	require.Len(t, r.targets, 1)
	require.Len(t, r.deferred, 1)
	require.Empty(t, r.marks)
}
func TestOwnerNoteLoss(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		lost bool
	}{
		{"nil", nil, false}, {"other", errors.New("other"), false},
		{"lost", ports.ErrDeviceLost, true}, {"wrapped", fmt.Errorf("wrapped: %w", ports.ErrDeviceLost), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Renderer{gpu: &GPU{}}
			require.Equal(t, tc.err, r.noteLoss(tc.err))
			require.Equal(t, tc.lost, r.lost)
			require.Equal(t, tc.lost, r.gpu.lost)
			c := &Copier{gpu: &GPU{}}
			require.Equal(t, tc.err, c.noteLoss(tc.err))
			require.Equal(t, tc.lost, c.lost)
			require.Equal(t, tc.lost, c.gpu.lost)
		})
	}
}
