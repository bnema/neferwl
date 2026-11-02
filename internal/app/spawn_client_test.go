package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"bnema.dev/wlturbo"
	clientcore "github.com/bnema/go-wayland-bindings/client/wayland"
	clientxdg "github.com/bnema/go-wayland-bindings/client/xdgshell"
	"golang.org/x/sys/unix"
)

const spawnClientArg = "--neferwl-test-spawn-client"

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == spawnClientArg {
		if err := runSpawnClient(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runSpawnClient is a real protocol client, re-executed by the launcher.
// Frame permission and buffer ownership are independent: when every buffer
// is busy it dispatches until a release, retaining the permission to draw.
func runSpawnClient() error {
	socket := os.Getenv("WAYLAND_DISPLAY")
	if !filepath.IsAbs(socket) {
		socket = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), socket)
	}
	c, err := wlturbo.Connect(socket)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Roundtrip(); err != nil {
		return err
	}
	comp := clientcore.NewCompositor(c.Context())
	shm := clientcore.NewShm(c.Context())
	wm := clientxdg.NewXdgWmBase(c.Context())
	for _, b := range []struct {
		name  string
		proxy wlturbo.Proxy
	}{{clientcore.CompositorInterface, comp}, {clientcore.ShmInterface, shm}, {clientxdg.XdgWmBaseInterface, wm}} {
		if _, err := c.Registry().BindNegotiated(b.name, 1, b.proxy); err != nil {
			return err
		}
	}
	var protocolErr error
	wm.OnPing(func(serial uint32) { protocolErr = wm.Pong(serial) })
	fd, err := unix.MemfdCreate("spawn-client", unix.MFD_CLOEXEC)
	if err != nil {
		return err
	}
	if err := unix.Ftruncate(fd, 12); err != nil {
		unix.Close(fd)
		return err
	}
	pool, err := shm.CreatePool(fd, 12)
	if err != nil {
		unix.Close(fd)
		return err
	}
	var buffers [3]*clientcore.Buffer
	var busy [3]bool
	for i := range buffers {
		buffers[i], err = pool.CreateBuffer(int32(i*4), 1, 1, 4, clientcore.FORMAT_ARGB8888)
		if err != nil {
			return err
		}
		buffers[i].OnRelease(func() { busy[i] = false })
	}
	surf, err := comp.CreateSurface()
	if err != nil {
		return err
	}
	xdg, err := wm.GetXdgSurface(surf)
	if err != nil {
		return err
	}
	configured := false
	xdg.OnConfigure(func(serial uint32) { protocolErr = xdg.AckConfigure(serial); configured = true })
	top, err := xdg.GetToplevel()
	if err != nil {
		return err
	}
	closed := false
	top.OnClose(func() { closed = true })
	if err := surf.Commit(); err != nil {
		return err
	}
	permission := true
	for !closed {
		if protocolErr != nil {
			return protocolErr
		}
		if configured && permission {
			for i, buffer := range buffers {
				if busy[i] {
					continue
				}
				if err := surf.Attach(buffer, 0, 0); err != nil {
					return err
				}
				if err := surf.Damage(0, 0, 1, 1); err != nil {
					return err
				}
				cb, err := surf.Frame()
				if err != nil {
					return err
				}
				cb.OnDone(func(uint32) { permission = true })
				busy[i], permission = true, false
				if err := surf.Commit(); err != nil {
					return err
				}
				break
			}
		}
		if err := c.Dispatch(); err != nil {
			return err
		}
	}
	return nil
}
