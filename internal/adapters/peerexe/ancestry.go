package peerexe

import (
	"os"
	"strconv"
	"strings"
)

// AncestorPIDs reads at most 32 parents, nearest first, stopping before init.
// Exited or inaccessible processes truncate the best-effort placement hint.
// Unlike ExeOfPidfd this is not an identity proof and must not authorize access.
func AncestorPIDs(pid int) []int {
	var chain []int
	seen := map[int]bool{pid: true}
	for range 32 {
		if pid <= 1 {
			break
		}
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			break
		}
		parent, ok := parentPID(string(stat))
		if !ok || parent <= 1 || seen[parent] {
			break
		}
		chain = append(chain, parent)
		seen[parent] = true
		pid = parent
	}
	return chain
}

// comm can contain spaces and parentheses; fields start after its final ')'.
func parentPID(stat string) (int, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 2 || len(fields[0]) != 1 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[1])
	return pid, err == nil && pid >= 0
}
