package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

func TestWatch(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "created later"}[later], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "config")
			if !later {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "config")
			ctx, cancel := context.WithCancel(context.Background())
			out := make(chan ports.ConfigChanged, 8)
			done := make(chan error, 1)
			old := directoryPollInterval
			directoryPollInterval = 20 * time.Millisecond
			defer func() { directoryPollInterval = old }()
			go func() { done <- Watch(ctx, path, out, logging.For(ctx, "config")) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			if later {
				time.Sleep(40 * time.Millisecond)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				time.Sleep(80 * time.Millisecond)
			} else {
				time.Sleep(40 * time.Millisecond)
			}
			write := func(file, body string) {
				t.Helper()
				if err := os.WriteFile(file, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(path, "background = #000000\n")
			// receive waits for want. A slow machine may pause longer than the
			// debounce between WriteFile's truncation and its write: the
			// watcher then reads an empty file first, which is skipped here.
			receive := func(want string) {
				t.Helper()
				deadline := time.After(time.Second)
				for {
					select {
					case c := <-out:
						if c.Config.Background.Color == want {
							return
						}
					case <-deadline:
						t.Fatalf("no config change to %s", want)
					}
				}
			}
			receive("#000000")
			tmp := filepath.Join(dir, "temp")
			write(tmp, "background = #ff0000\n")
			if err := os.Rename(tmp, path); err != nil {
				t.Fatal(err)
			}
			receive("#ff0000")
			for i := 0; i < 3; i++ {
				write(path, "background = #00ff00\n")
				time.Sleep(15 * time.Millisecond)
			}
			receive("#00ff00")
			// A loaded machine may pause past the debounce inside the burst
			// and reload the truncated file in between: that is a change, not a
			// duplicate. Only the same config sent twice in a row is one.
			prev := "#00ff00"
			for quiet := false; !quiet; {
				select {
				case c := <-out:
					if c.Config.Background.Color == prev {
						t.Fatal("duplicate reload")
					}
					prev = c.Config.Background.Color
				case <-time.After(250 * time.Millisecond):
					quiet = true
				}
			}
			// An invalid value keeps the default for that key; other keys still apply.
			write(path, "background = bad\nborder.width = 5\n")
			select {
			case c := <-out:
				if c.Config.Background.Color != "#111111" || c.Config.Border.Width != 5 {
					t.Fatalf("partial reload: %+v", c.Config)
				}
			case <-time.After(time.Second):
				t.Fatal("no partial reload")
			}
			// Rewriting the same content sends nothing.
			write(path, "background = bad\nborder.width = 5\n")
			select {
			case <-out:
				t.Fatal("unchanged config sent")
			case <-time.After(300 * time.Millisecond):
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			select {
			case <-out:
				t.Fatal("removed config sent")
			case <-time.After(250 * time.Millisecond):
			}
		})
	}
}

// A config symlinked elsewhere (dotfiles), directly or through a chain,
// reloads when its target changes, follows a retargeted link, and survives
// a target that is missing for a while.
func TestWatchSymlink(t *testing.T) {
	root := t.TempDir()
	dirs := map[string]string{}
	for _, d := range []string{"config", "dotfiles", "chain", "other"} {
		dirs[d] = filepath.Join(root, d)
		if err := os.Mkdir(dirs[d], 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, at string) {
		t.Helper()
		tmp := at + ".tmp"
		if err := os.Symlink(target, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, at); err != nil {
			t.Fatal(err)
		}
	}
	// config/config -> chain/config (relative) -> dotfiles/neferwl.conf
	target := filepath.Join(dirs["dotfiles"], "neferwl.conf")
	write(target, "background = #000000\n")
	middle := filepath.Join(dirs["chain"], "config")
	link("../dotfiles/neferwl.conf", middle)
	path := filepath.Join(dirs["config"], "config")
	link(middle, path)
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan ports.ConfigChanged, 8)
	done := make(chan error, 1)
	old := directoryPollInterval
	directoryPollInterval = 20 * time.Millisecond
	defer func() { directoryPollInterval = old }()
	go func() { done <- Watch(ctx, path, out, logging.For(ctx, "config")) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	// until repeats change until the config reloads to want: the watches
	// may not be set up yet on a slow machine. Each try changes another key
	// too, as the watcher may have read the first try at startup.
	tries := 0
	until := func(want string, change func(body string)) {
		t.Helper()
		try := func() {
			tries++
			change(fmt.Sprintf("background = %s\nborder.width = %d\n", want, tries%32))
		}
		try()
		deadline := time.After(2 * time.Second)
		retry := time.NewTicker(300 * time.Millisecond)
		defer retry.Stop()
		for {
			select {
			case c := <-out:
				if c.Config.Background.Color == want {
					return
				}
			case <-retry.C:
				try()
			case <-deadline:
				t.Fatalf("no config change to %s", want)
			}
		}
	}
	atomic := func(file string) func(string) {
		return func(body string) {
			write(file+".new", body)
			if err := os.Rename(file+".new", file); err != nil {
				t.Fatal(err)
			}
		}
	}
	until("#ff0000", func(body string) { write(target, body) })
	until("#00ff00", atomic(target))
	// The middle link now points to another file, once: later writes to it
	// reload only if the watch moved there.
	moved := filepath.Join(dirs["other"], "config")
	write(moved, "background = #0000ff\n")
	link(moved, middle)
	until("#0000ff", func(string) {})
	until("#ffffff", func(body string) { write(moved, body) })
	// The target disappears with its directory, then comes back once: later
	// writes reload only if the missing directory was watched again.
	if err := os.RemoveAll(dirs["other"]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := os.Mkdir(dirs["other"], 0700); err != nil {
		t.Fatal(err)
	}
	write(moved, "background = #000001\n")
	time.Sleep(100 * time.Millisecond) // past the 20 ms retry
	until("#abcdef", func(body string) { write(moved, body) })
	// The old target no longer matters.
	write(target, "background = #123456\n")
	select {
	case c := <-out:
		t.Fatalf("old target reloaded: %+v", c.Config.Background)
	case <-time.After(250 * time.Millisecond):
	}
}

func TestWatchedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if got := watchedFiles(path); !slices.Equal(got, []string{path}) {
		t.Fatal(got)
	}
	// A dangling link still names its target; a loop stops.
	if err := os.Symlink("sub/../missing", path); err != nil {
		t.Fatal(err)
	}
	if got := watchedFiles(path); !slices.Equal(got, []string{path, filepath.Join(dir, "missing")}) {
		t.Fatal(got)
	}
	if err := os.Symlink("config", filepath.Join(dir, "missing")); err != nil {
		t.Fatal(err)
	}
	if got := watchedFiles(path); !slices.Equal(got, []string{path, filepath.Join(dir, "missing")}) {
		t.Fatal(got)
	}
}
