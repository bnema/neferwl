package wayland

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"golang.org/x/sys/unix"
)

// listen reports cleanup failures to log (the zero Logger discards them).
func listen(runtimeDir string, log zerowrap.Logger) (name string, fd int, cleanup func(), err error) {
	for n := 1; n <= 32; n++ {
		name = fmt.Sprintf("wayland-%d", n)
		path := filepath.Join(runtimeDir, name)
		// Validate the public address, not just the shorter staging address.
		// Leave room for the terminating NUL, as unix.Bind would require.
		if len(path) >= len(unix.RawSockaddrUnix{}.Path) {
			return "", -1, nil, fmt.Errorf("wayland socket path too long: %s: %w", path, unix.EINVAL)
		}
		// Keep the staging basename shorter than wayland-N: adding a suffix
		// would reject otherwise valid paths at the Unix sun_path limit.
		tmp := filepath.Join(runtimeDir, fmt.Sprintf(".wl-%d", n))
		lock := path + ".lock"
		l, e := unix.Open(lock, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC, 0660)
		if e != nil {
			return "", -1, nil, e
		}
		if e = unix.Flock(l, unix.LOCK_EX|unix.LOCK_NB); e != nil {
			cerr := unix.Close(l)
			if e == unix.EWOULDBLOCK {
				if cerr != nil {
					log.Warn().Err(cerr).Str("lock", lock).Msg("close socket lock")
				}
				continue
			}
			return "", -1, nil, errors.Join(e, cerr)
		}
		cleanup = func() {
			for _, p := range []string{tmp, path, lock} {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					log.Warn().Err(err).Str("path", p).Msg("remove wayland socket file")
				}
			}
			logging.CloseFD(log, l, "socket lock")
		}
		if e = os.Remove(tmp); e != nil && !os.IsNotExist(e) {
			cleanup()
			return "", -1, nil, e
		}
		if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
			cleanup()
			return "", -1, nil, e
		}
		fd, e = unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if e == nil {
			e = unix.Bind(fd, &unix.SockaddrUnix{Name: tmp})
		}
		if e == nil {
			e = unix.Listen(fd, 128)
		}
		if e == nil {
			// Publish only a listening socket to clients watching wayland-N.
			e = unix.Rename(tmp, path)
		}
		if e != nil {
			if fd >= 0 {
				e = errors.Join(e, unix.Close(fd))
			}
			cleanup()
			return "", -1, nil, e
		}
		return name, fd, cleanup, nil
	}
	return "", -1, nil, fmt.Errorf("no free wayland socket in %s", runtimeDir)
}
