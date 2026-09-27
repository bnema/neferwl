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
