package vulkan

import (
	"errors"
	"testing"

	vk "bnema.dev/purego-vulkan/vulkan"
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
