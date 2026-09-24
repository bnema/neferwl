package wayland

import (
	"bytes"
	"os"
	"strconv"

	"github.com/bnema/nefertty/internal/ports"
)

// procEnv reads one environment variable of a running process.
type procEnv interface {
	Lookup(pid int, key string) (string, bool)
}

// linuxProcEnv reads /proc/<pid>/environ: the environment the process started
// with. A slot command that forks its window process passes it down, since
// children inherit the environment.
type linuxProcEnv struct{}

func (linuxProcEnv) Lookup(pid int, key string) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", false
	}
	prefix := []byte(key + "=")
	for entry := range bytes.SplitSeq(data, []byte{0}) {
		if v, ok := bytes.CutPrefix(entry, prefix); ok {
			return string(v), true
		}
	}
	return "", false
}

// slotToken returns the SlotEnv value of the client process, if any.
func (s *Server) slotToken(pid int) string {
	v, _ := s.env.Lookup(pid, ports.SlotEnv)
	return v
}
