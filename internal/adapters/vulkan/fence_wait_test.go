package vulkan

import (
	vk "bnema.dev/purego-vulkan/vulkan"
	"errors"
	"github.com/bnema/neferwl/internal/ports"
	"strings"
	"testing"
)

func TestFenceWaitErr(t *testing.T) {
	for _, tt := range []struct {
		name                   string
		result                 vk.Result
		wantErr, lost, timeout bool
	}{
		{"timeout", vk.Timeout, true, true, true},
		{"success", vk.Success, false, false, false},
		{"device lost", vk.ErrorDeviceLost, true, true, false},
		{"out of memory", vk.ErrorOutOfDeviceMemory, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := fenceWaitErr("test wait", tt.result)
			if (err != nil) != tt.wantErr || errors.Is(err, ports.ErrDeviceLost) != tt.lost || errors.Is(err, errFenceTimeout) != tt.timeout {
				t.Fatalf("unexpected error: %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "test wait") {
				t.Fatalf("missing wait name: %v", err)
			}
		})
	}
}
