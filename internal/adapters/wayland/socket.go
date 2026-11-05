package wayland

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func listen(runtimeDir string) (name string, fd int, cleanup func(), err error) {
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
			unix.Close(l)
			if e == unix.EWOULDBLOCK {
				continue
			}
			return "", -1, nil, e
		}
		cleanup = func() { os.Remove(tmp); os.Remove(path); os.Remove(lock); unix.Close(l) }
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
				unix.Close(fd)
			}
			cleanup()
			return "", -1, nil, e
		}
		return name, fd, cleanup, nil
	}
	return "", -1, nil, fmt.Errorf("no free wayland socket in %s", runtimeDir)
}
