package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unsafe"

	"github.com/bnema/nefertty/internal/ports"
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

// Watch observes the parent directory so atomic file replacements are detected.
// If the directory is absent, it retries every two seconds until it appears.
func Watch(ctx context.Context, path string, out chan<- ports.ConfigChanged, log zerowrap.Logger) error {
	dir, name := filepath.Dir(path), filepath.Base(path)
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	const mask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM
	wd := -1
	var pending time.Time
	last := loadRaw(path)
	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if wd < 0 {
			wd, err = unix.InotifyAddWatch(fd, dir, mask)
			if errors.Is(err, unix.ENOENT) {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(directoryPollInterval):
					continue
				}
			}
			if err != nil {
				return err
			}
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
				if event.Mask&unix.IN_IGNORED != 0 {
					wd = -1
				}
				if event.Len > 0 {
					file := buf[offset+unix.SizeofInotifyEvent : offset+size]
					for len(file) > 0 && file[len(file)-1] == 0 {
						file = file[:len(file)-1]
					}
					if string(file) == name && event.Mask&mask != 0 {
						pending = time.Now().Add(100 * time.Millisecond)
					}
				}
				offset += size
			}
		}
	}
}
