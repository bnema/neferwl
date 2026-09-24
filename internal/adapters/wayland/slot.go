package wayland

import (
	"bytes"
	"os"
	"strconv"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/purego-libwayland/server"
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

// slotToken returns the SlotEnv value of the client process, if any. It
// reads /proc only while core waits for a slot window. There is no per-client
// cache: libwayland may reuse a client address after a disconnect.
func (s *Server) slotToken(c server.Client) string {
	if !s.slotsPending {
		return ""
	}
	v, _ := s.env.Lookup(c.PID(), ports.SlotEnv)
	return v
}
