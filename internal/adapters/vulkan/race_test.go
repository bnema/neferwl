//go:build race

package vulkan

// raceEnabled: purego keeps call arguments in a sync.Pool, which drops items
// at random under the race detector, so code that crosses into Vulkan
// allocates a varying amount and its allocation count is not meaningful.
const raceEnabled = true
