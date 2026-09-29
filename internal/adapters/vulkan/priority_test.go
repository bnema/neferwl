package vulkan

import (
	"slices"
	"testing"

	vk "github.com/bnema/purego-vulkan/vulkan"
)

func TestPriorityAttempts(t *testing.T) {
	if got := priorityAttempts(true); !slices.Equal(got, []vk.QueueGlobalPriority{vk.QueueGlobalPriorityRealtime, vk.QueueGlobalPriorityHigh, 0}) {
		t.Fatal(got)
	}
	if got := priorityAttempts(false); !slices.Equal(got, []vk.QueueGlobalPriority{0}) {
		t.Fatal(got)
	}
}
