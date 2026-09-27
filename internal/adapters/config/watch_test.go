package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
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
			select {
			case <-out:
				t.Fatal("duplicate reload")
			case <-time.After(250 * time.Millisecond):
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

// A config symlinked into another directory (dotfiles) reloads when its
// target changes, in place or atomically, and follows a retargeted link.
func TestWatchSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	dotfiles := filepath.Join(root, "dotfiles")
	other := filepath.Join(root, "other")
	for _, d := range []string{dir, dotfiles, other} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dotfiles, "neferwl.conf")
	write(target, "background = #000000\n")
	path := filepath.Join(dir, "config")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan ports.ConfigChanged, 8)
	done := make(chan error, 1)
	go func() { done <- Watch(ctx, path, out, logging.For(ctx, "config")) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	time.Sleep(40 * time.Millisecond)
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
	// In place.
	write(target, "background = #ff0000\n")
	receive("#ff0000")
	// Atomic replacement of the target.
	tmp := filepath.Join(dotfiles, ".neferwl.conf.tmp")
	write(tmp, "background = #00ff00\n")
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}
	receive("#00ff00")
	// The link now points to another file: it is read and then watched.
	moved := filepath.Join(other, "config")
	write(moved, "background = #0000ff\n")
	link := filepath.Join(dir, ".config.tmp")
	if err := os.Symlink(moved, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(link, path); err != nil {
		t.Fatal(err)
	}
	receive("#0000ff")
	write(moved, "background = #ffffff\n")
	receive("#ffffff")
	// The old target no longer matters.
	write(target, "background = #123456\n")
	select {
	case c := <-out:
		t.Fatalf("old target reloaded: %+v", c.Config.Background)
	case <-time.After(250 * time.Millisecond):
	}
}
