package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

var directoryPollInterval = 2 * time.Second

func loadFile(path string) (ports.Config, map[string]string, []Warning, error) {
	f, err := os.Open(path)
	if err != nil {
		return ports.Config{}, nil, nil, err
	}
	defer f.Close()
	return parse(f)
}

// loadRaw returns the effective keys of the file at startup; missing means none.
func loadRaw(path string) map[string]string {
	_, raw, _, err := loadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return raw
}

// diff lists keys added, removed or changed, sorted.
func diff(old, cur map[string]string) []string {
	var keys []string
	for k, v := range cur {
		if o, ok := old[k]; !ok || o != v {
			keys = append(keys, k)
		}
	}
	for k := range old {
		if _, ok := cur[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// watchedFiles are the files whose changes reload the config: path itself
// and, when path goes through symlinks (dotfiles), the file they resolve to.
func watchedFiles(path string) []string {
	files := []string{filepath.Clean(path)}
	if target, err := filepath.EvalSymlinks(path); err == nil && target != files[0] {
		files = append(files, target)
	}
	return files
}

// Watch observes the parent directory so atomic file replacements are detected.
// A symlinked config is also watched at its target, and a retargeted link
// moves that watch. If the directory is absent, it retries every two seconds
// until it appears.
func Watch(ctx context.Context, path string, out chan<- ports.ConfigChanged, log zerowrap.Logger) error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// IN_MODIFY restarts the debounce on every write, the truncation of an
	// in-place save included: the file is never read half written.
	const mask = unix.IN_CLOSE_WRITE | unix.IN_MODIFY | unix.IN_MOVED_TO | unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM
	// names maps each watch to the file names that matter in its directory.
	names := map[int32]map[string]bool{}
	var files []string
	rewatch := true
	var pending time.Time
	last := loadRaw(path)
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if rewatch {
			for wd := range names {
				_, _ = unix.InotifyRmWatch(fd, uint32(wd))
			}
			clear(names)
			files = watchedFiles(path)
			missing := false
			for i, file := range files {
				wd, err := unix.InotifyAddWatch(fd, filepath.Dir(file), mask)
				if errors.Is(err, unix.ENOENT) {
					// Only the configured directory is waited for; a
					// target directory shows up through the link.
					missing = missing || i == 0
					continue
				}
				if err != nil {
					return err
				}
				if names[int32(wd)] == nil {
					names[int32(wd)] = map[string]bool{}
				}
				names[int32(wd)][filepath.Base(file)] = true
			}
			if missing {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(directoryPollInterval):
					continue
				}
			}
			rewatch = false
		}
		timeout := 200
		if !pending.IsZero() {
			remaining := time.Until(pending)
			if remaining <= 0 {
				pending = time.Time{}
				cfg, raw, warnings, loadErr := loadFile(path)
				switch {
				case os.IsNotExist(loadErr):
					log.Warn().Msg("config file removed; keeping current config")
				case loadErr != nil:
					log.Warn().Err(loadErr).Msg("config reload failed")
				default:
					for _, w := range warnings {
						log.Warn().Int("line", w.Line).Msg(w.Msg)
					}
					changed := diff(last, raw)
					last = raw
					if len(changed) == 0 {
						log.Debug().Msg("config unchanged")
						continue
					}
					select {
					case <-ctx.Done():
						return nil
					case out <- ports.ConfigChanged{Config: cfg}:
					}
					log.Info().Strs("changed", changed).Msg("config reloaded")
				}
				continue
			}
			if remaining < 200*time.Millisecond {
				timeout = int(remaining.Milliseconds()) + 1
			}
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err = unix.Poll(fds, timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		for {
			n, readErr := unix.Read(fd, buf)
			if readErr == unix.EAGAIN {
				break
			}
			if readErr == unix.EINTR {
				continue
			}
			if readErr != nil {
				return readErr
			}
			if n == 0 {
				break
			}
			for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
				event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
				size := unix.SizeofInotifyEvent + int(event.Len)
				if offset+size > n {
					break
				}
				// A removed watch of ours (its directory is gone) is set up
				// again; removals we asked for are no longer in names.
				if event.Mask&unix.IN_IGNORED != 0 && names[event.Wd] != nil {
					rewatch = true
				}
				if event.Len > 0 {
					file := buf[offset+unix.SizeofInotifyEvent : offset+size]
					for len(file) > 0 && file[len(file)-1] == 0 {
						file = file[:len(file)-1]
					}
					if names[event.Wd][string(file)] && event.Mask&mask != 0 {
						pending = time.Now().Add(100 * time.Millisecond)
						// The link may point elsewhere now.
						if !slices.Equal(files, watchedFiles(path)) {
							rewatch = true
						}
					}
				}
				offset += size
			}
		}
	}
}
