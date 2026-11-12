package vulkan

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gpuEngineTime sums this process's DRM engine execution counters, not time
// queued behind other GPU clients. Duplicate fds for a client count only once.
func gpuEngineTime(t testing.TB) uint64 {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fdinfo")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	var total uint64
	found := false
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join("/proc/self/fdinfo", entry.Name()))
		if err != nil {
			continue
		} // The directory read itself can leave a closed fd.
		lines := strings.Split(string(data), "\n")
		var client, device string
		for _, line := range lines {
			if strings.HasPrefix(line, "drm-client-id:") {
				client = strings.TrimSpace(strings.TrimPrefix(line, "drm-client-id:"))
			}
			if strings.HasPrefix(line, "drm-pdev:") {
				device = strings.TrimSpace(strings.TrimPrefix(line, "drm-pdev:"))
			}
		}
		key := device + ":" + client
		if client == "" || seen[key] {
			continue
		}
		seen[key] = true
		for _, line := range lines {
			if !strings.HasPrefix(line, "drm-engine-") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) != 3 || fields[2] != "ns" {
				t.Fatalf("unexpected DRM engine counter: %q", line)
			}
			n, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				t.Fatalf("parse DRM engine counter %q: %v", line, err)
			}
			total += n
			found = true
		}
	}
	if !found {
		t.Skip("DRM engine execution counters unavailable")
	}
	return total
}
