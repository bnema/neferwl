package captureallow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// load reads the file at path, which must be trustworthy. owner is the uid
// that must own the file, and may own its directories besides root.
// Production passes 0; tests pass their own uid. A missing file gives the
// built-in policy and an error wrapping fs.ErrNotExist; any other failure gives
// the closed policy and the error, so a broken or tampered-with file never
// opens capture.
//
// The file is opened without following a symlink and without blocking (a FIFO
// must not hang the compositor), then checked on the descriptor: a regular
// file, owned by owner, writable by nobody else. Every directory from / down
// to the file's own must be a real directory (not a symlink), owned by root or
// owner, and not group- or other-writable unless sticky. Anything else fails
// closed. Only a missing file, or a missing directory that holds the file
// (/etc/neferwl) under checked ancestors, gives the built-in list.
func load(path string, owner uint32) (*Policy, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return Closed(), fmt.Errorf("%s: path is not absolute", path)
	}
	missing, err := checkDirs(path, owner)
	if err != nil {
		return Closed(), err
	}
	if missing {
		return Builtin(), &fs.PathError{Op: "open", Path: path, Err: unix.ENOENT}
	}
	var fd int
	for {
		fd, err = unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return Builtin(), &fs.PathError{Op: "open", Path: path, Err: unix.ENOENT}
		}
		if errors.Is(err, unix.ELOOP) {
			return Closed(), fmt.Errorf("%s: is a symbolic link", path)
		}
		return Closed(), &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Closed(), &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return Closed(), fmt.Errorf("%s: not a regular file", path)
	case st.Uid != owner:
		return Closed(), fmt.Errorf("%s: owned by uid %d, want %d", path, st.Uid, owner)
	case st.Mode&0o022 != 0:
		return Closed(), fmt.Errorf("%s: writable by group or others (mode %04o)", path, st.Mode&0o7777)
	}
	// Reads of a regular file do not block; leave non-blocking mode off so the
	// os.File does not go through the poller.
	_ = unix.SetNonblock(fd, false)
	p, err := Parse(f)
	if err != nil {
		return Closed(), err
	}
	return p, nil
}

// checkDirs checks every directory from / to the one holding path. It reports
// missing when that last directory does not exist.
func checkDirs(path string, owner uint32) (missing bool, err error) {
	var chain []string
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		chain = append(chain, d)
		if d == "/" {
			break
		}
	}
	slices.Reverse(chain)
	for i, d := range chain {
		fi, err := os.Lstat(d)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && i == len(chain)-1 {
				return true, nil
			}
			return false, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		switch {
		case !fi.IsDir():
			return false, fmt.Errorf("%s: not a real directory (symbolic link?)", d)
		case !ok:
			return false, fmt.Errorf("%s: owner unknown", d)
		case st.Uid != 0 && st.Uid != owner:
			return false, fmt.Errorf("%s: owned by uid %d, want root", d, st.Uid)
		case fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0:
			return false, fmt.Errorf("%s: writable by group or others (mode %04o)", d, fi.Mode().Perm())
		}
	}
	return false, nil
}

// Store holds the current policy and reloads it when the file changes. Policy
// is safe from any goroutine.
type Store struct {
	path  string
	owner uint32
	log   zerowrap.Logger
	cur   atomic.Pointer[Policy]
	// retryInterval is the wait before watching a missing directory again.
	retryInterval time.Duration
}

// NewStore loads path once and logs the outcome: an Info when the file is
// missing (the built-in list is then in force), an Error naming the line when it is
// invalid (no client may capture). Run keeps it current.
func NewStore(path string, log zerowrap.Logger) *Store {
	return NewStoreOwnedBy(path, 0, log)
}

// NewStoreOwnedBy is NewStore for a file owned by owner instead of root, which
// then may also own its directories. For tests only, which cannot create
// root-owned files: the compositor uses NewStore.
func NewStoreOwnedBy(path string, owner uint32, log zerowrap.Logger) *Store {
	s := &Store{path: path, owner: owner, log: log, retryInterval: 2 * time.Second}
	s.reload(true, false)
	return s
}

// Policy is the policy in force.
func (s *Store) Policy() *Policy { return s.cur.Load() }

// reload loads the file and logs the outcome. With quiet set it logs only
// when the policy changed: the check that closes the gap between the first
// load and the watch must not repeat the startup line.
func (s *Store) reload(startup, quiet bool) {
	p, err := load(s.path, s.owner)
	old := s.cur.Swap(p)
	if quiet && old != nil && old.equal(p) {
		return
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.log.Info().Str("path", s.path).Msg("capture allowlist file missing, using the built-in list")
	case err != nil:
		e := s.log.Error().Err(err).Str("path", s.path)
		var pe *ParseError
		if errors.As(err, &pe) {
			e = e.Int("line", pe.Line)
		}
		e.Msg("capture allowlist invalid, no client may capture")
	case p.Mode() == ModeDisabled:
		s.log.Warn().Str("path", s.path).Msg("capture allowlist disables the check, every client may capture")
	case startup:
		s.log.Info().Str("path", s.path).Int("executables", p.Len()).Msg("capture allowlist loaded")
	default:
		s.log.Info().Str("path", s.path).Int("executables", p.Len()).Msg("capture allowlist reloaded")
	}
}

// Run reloads the file whenever it changes, until ctx is done. It watches the
// parent directory, so an atomic replacement is seen; a symlinked file's
// target is not watched. While the directory is missing it retries every two
// seconds. An error means the watch could not be set up: the policy then
// stays as loaded ("restart to apply").
func (s *Store) Run(ctx context.Context) error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	dir, name := filepath.Dir(s.path), filepath.Base(s.path)
	wd := -1
	var retry, pending time.Time
	quiet := false
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if wd < 0 && (retry.IsZero() || !time.Now().Before(retry)) {
			retry = time.Time{}
			w, err := unix.InotifyAddWatch(fd, dir, watchMask)
			switch {
			case err == nil:
				wd = w
				// The file may have changed before the watch existed.
				pending, quiet = time.Now(), true
			case errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR):
				retry = time.Now().Add(s.retryInterval)
			default:
				return err
			}
		}
		timeout := 200
		if !retry.IsZero() {
			timeout = min(timeout, int(time.Until(retry).Milliseconds())+1)
		}
		if !pending.IsZero() {
			remaining := time.Until(pending)
			if remaining <= 0 {
				pending = time.Time{}
				s.reload(false, quiet)
				quiet = false
				continue
			}
			timeout = min(timeout, int(remaining.Milliseconds())+1)
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, timeout); err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		for {
			n, err := unix.Read(fd, buf)
			if err == unix.EINTR {
				continue
			}
			if err != nil || n == 0 {
				break
			}
			for off := 0; off+unix.SizeofInotifyEvent <= n; {
				ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
				size := unix.SizeofInotifyEvent + int(ev.Len)
				if off+size > n {
					break
				}
				reload, lost := eventAction(ev.Mask, buf[off+unix.SizeofInotifyEvent:off+size], name)
				if lost {
					// The directory is gone: watch again once it is back.
					wd = -1
					retry = time.Now().Add(s.retryInterval)
				}
				if reload {
					// Every write restarts the debounce: never read half a file.
					pending, quiet = time.Now().Add(100*time.Millisecond), false
				}
				off += size
			}
		}
	}
}

// watchMask is what the directory watch listens for.
const watchMask = unix.IN_CLOSE_WRITE | unix.IN_MODIFY | unix.IN_MOVED_TO | unix.IN_MOVED_FROM | unix.IN_CREATE | unix.IN_DELETE | unix.IN_ATTRIB

// eventAction says what one inotify event asks for: reload the file, and
// watch the directory again because it was removed. A queue overflow means
// events were lost, so the file is reloaded whatever it was.
func eventAction(mask uint32, raw []byte, name string) (reload, lost bool) {
	if mask&unix.IN_Q_OVERFLOW != 0 {
		return true, false
	}
	if mask&unix.IN_IGNORED != 0 {
		reload, lost = true, true
	}
	for len(raw) > 0 && raw[len(raw)-1] == 0 {
		raw = raw[:len(raw)-1]
	}
	switch {
	case len(raw) == 0 && mask&unix.IN_ATTRIB != 0:
		reload = true // the directory itself was chmod'ed or chown'ed
	case len(raw) > 0 && mask&watchMask != 0 && string(raw) == name:
		reload = true
	}
	return reload, lost
}
