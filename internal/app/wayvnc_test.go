//go:build wayvnc

// End-to-end test of remote viewing and control with wayvnc: a headless
// NeferWL, a real wayvnc (wlr-screencopy, virtual pointer, virtual
// keyboard) and a minimal RFB client in the test. It needs wayvnc on PATH
// and a GPU, so it is excluded from `go test ./...`. Run it with
// `make e2e-wayvnc`.
package app

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bnema.dev/wlturbo"
	"github.com/bnema/neferwl/internal/adapters/config"
	"golang.org/x/sys/unix"
)

const vncW, vncH = 320, 240

// rfb is a minimal RFB 3.8 client: no authentication, raw encoding, 32-bit
// little-endian true colour (blue, green, red, pad).
type rfb struct {
	conn net.Conn
	r    *bufio.Reader
	w, h int
}

// dialRFB connects and initialises a shared session; a socket deadline
// bounds every later read and write.
func dialRFB(t *testing.T, addr string) *rfb {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	c := &rfb{conn: conn, r: bufio.NewReader(conn)}
	version := make([]byte, 12)
	c.read(t, version)
	if !strings.HasPrefix(string(version), "RFB 003.") {
		t.Fatalf("server version %q", version)
	}
	c.write(t, []byte("RFB 003.008\n"))
	n := c.u8(t)
	if n == 0 {
		reason := make([]byte, c.u32(t))
		c.read(t, reason)
		t.Fatalf("connection refused: %s", reason)
	}
	types := make([]byte, n)
	c.read(t, types)
	if !strings.ContainsRune(string(types), 1) {
		t.Fatalf("no None security type in %v (wayvnc must run without auth)", types)
	}
	c.write(t, []byte{1})
	if res := c.u32(t); res != 0 {
		t.Fatalf("security result %d", res)
	}
	c.write(t, []byte{1}) // shared
	c.w, c.h = int(c.u16(t)), int(c.u16(t))
	c.read(t, make([]byte, 16)) // server pixel format
	c.read(t, make([]byte, c.u32(t)))
	// SetPixelFormat: 32 bpp, depth 24, little endian, true colour,
	// max 255 each, shifts red 16, green 8, blue 0.
	pf := []byte{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}
	c.write(t, pf)
	c.write(t, []byte{2, 0, 0, 1, 0, 0, 0, 0}) // SetEncodings: raw only
	return c
}

func (c *rfb) read(t *testing.T, b []byte) {
	t.Helper()
	if _, err := io.ReadFull(c.r, b); err != nil {
		t.Fatal(err)
	}
}

func (c *rfb) write(t *testing.T, b []byte) {
	t.Helper()
	if _, err := c.conn.Write(b); err != nil {
		t.Fatal(err)
	}
}

func (c *rfb) u8(t *testing.T) byte { b := make([]byte, 1); c.read(t, b); return b[0] }
func (c *rfb) u16(t *testing.T) uint16 {
	b := make([]byte, 2)
	c.read(t, b)
	return binary.BigEndian.Uint16(b)
}
func (c *rfb) u32(t *testing.T) uint32 {
	b := make([]byte, 4)
	c.read(t, b)
	return binary.BigEndian.Uint32(b)
}

// frame requests a full update and returns the framebuffer as 0xRRGGBB.
func (c *rfb) frame(t *testing.T) []uint32 {
	t.Helper()
	req := []byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(req[6:], uint16(c.w))
	binary.BigEndian.PutUint16(req[8:], uint16(c.h))
	c.write(t, req)
	fb := make([]uint32, c.w*c.h)
	for {
		switch typ := c.u8(t); typ {
		case 0:
			c.read(t, make([]byte, 1))
			for n := c.u16(t); n > 0; n-- {
				x, y, w, h := int(c.u16(t)), int(c.u16(t)), int(c.u16(t)), int(c.u16(t))
				if enc := int32(c.u32(t)); enc != 0 {
					// Pseudo-encodings carry no pixels we asked for.
					if w*h != 0 {
						t.Fatalf("unexpected encoding %d", enc)
					}
					continue
				}
				row := make([]byte, w*4)
				for j := 0; j < h; j++ {
					c.read(t, row)
					for i := 0; i < w; i++ {
						if x+i < c.w && y+j < c.h {
							fb[(y+j)*c.w+x+i] = binary.LittleEndian.Uint32(row[i*4:]) & 0xffffff
						}
					}
				}
			}
			return fb
		case 2: // bell
		case 3: // server cut text
			c.read(t, make([]byte, 3))
			c.read(t, make([]byte, c.u32(t)))
		default:
			t.Fatalf("unexpected server message %d", typ)
		}
	}
}

func (c *rfb) pointer(t *testing.T, mask byte, x, y int) {
	t.Helper()
	b := []byte{5, mask, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(x))
	binary.BigEndian.PutUint16(b[4:], uint16(y))
	c.write(t, b)
}

func (c *rfb) key(t *testing.T, keysym uint32, down bool) {
	t.Helper()
	b := []byte{4, 0, 0, 0, 0, 0, 0, 0}
	if down {
		b[1] = 1
	}
	binary.BigEndian.PutUint32(b[4:], keysym)
	c.write(t, b)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestWayVNC: wayvnc captures the headless output and its remote pointer and
// keyboard drive the session through the virtual pointer and keyboard.
//
//testpolicy:realtime real headless compositor, wayvnc child process and sockets
func TestWayVNC(t *testing.T) {
	wayvnc, err := exec.LookPath("wayvnc")
	if err != nil {
		t.Skip("wayvnc not on PATH")
	}
	if real, err := filepath.EvalSymlinks(wayvnc); err == nil {
		wayvnc = real
	}
	for _, k := range []string{"NEFERWL_STATE", "NEFERWL_SLOT", "WAYLAND_DISPLAY"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no user wayvnc config (auth)
	allowPath := filepath.Join(t.TempDir(), "capture-allow")
	if err := os.WriteFile(allowPath, []byte(wayvnc+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Animations.On = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, PlanOverrides: noTerminal(), Config: cfg, Sizes: [][2]int{{vncW, vncH}}, captureAllowPath: allowPath, captureAllowOwner: uint32(os.Getuid())})
	}()
	stopped := false
	defer func() {
		cancel()
		if stopped {
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	}()
	socket := filepath.Join(runtime, "wayland-1")
	waitFile := fileChangesWithin(t, runtime, 5*time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case err := <-done:
			stopped = true
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "vulkan") {
				t.Skipf("Vulkan unavailable: %v", err)
			}
			t.Fatalf("neferwl exited: %v", err)
		default:
		}
		if !waitFile() {
			t.Fatal("no wayland socket")
		}
	}

	c, err := wlturbo.Connect(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target := mapPointerTarget(t, c)
	dispatchUntil(t, c, 5*time.Second, func() string { return "no keyboard focus" }, func() bool { return target.focused })

	port := freePort(t)
	cmd := exec.Command(wayvnc, "-L", "info", "-S", filepath.Join(runtime, "wayvncctl"), "-o", "HEADLESS-1", "127.0.0.1", strconv.Itoa(port))
	cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY=wayland-1")
	out := &lockedBuffer{changed: make(chan struct{}, 1)}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Signal(unix.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	}()
	ready := time.After(5 * time.Second)
	for !strings.Contains(out.String(), "Listening") {
		select {
		case err := <-exited:
			exited <- err
			t.Fatalf("wayvnc exited: %v: %s", err, out.String())
		default:
		}
		if !waitProcessOutputUntil(out, ready) {
			t.Fatalf("wayvnc not listening: %s", out.String())
		}
	}

	vnc := dialRFB(t, fmt.Sprintf("127.0.0.1:%d", port))
	if vnc.w != vncW || vnc.h != vncH {
		t.Fatalf("framebuffer %dx%d, want %dx%d", vnc.w, vnc.h, vncW, vncH)
	}
	// The red square shows in the remote framebuffer; aim at its centre.
	var fb []uint32
	rx, ry, n := 0, 0, 0
	for attempt := 0; attempt < 10 && n == 0; attempt++ {
		fb = vnc.frame(t)
		for i, p := range fb {
			if p == 0xff0000 {
				rx, ry, n = rx+i%vnc.w, ry+i/vnc.w, n+1
			}
		}
	}
	if n == 0 {
		t.Fatalf("client content missing from the framebuffer: %s", out.String())
	}
	rx, ry = rx/n, ry/n
	t.Logf("red square centre %d,%d (%d px)", rx, ry, n)

	vnc.pointer(t, 0, rx, ry)
	vnc.pointer(t, 1, rx, ry)
	vnc.pointer(t, 0, rx, ry)
	dispatchUntil(t, c, 5*time.Second, func() string {
		return fmt.Sprintf("entered %v pressed %v released %v; wayvnc: %s", target.entered, target.pressed, target.release, out.String())
	}, func() bool { return target.entered && target.release })

	vnc.key(t, 'a', true)
	vnc.key(t, 'a', false)
	dispatchUntil(t, c, 5*time.Second, func() string { return fmt.Sprintf("keys %v; wayvnc: %s", target.keys, out.String()) }, func() bool { return len(target.keys) > 0 })
	if target.keys[0] != 30 { // KEY_A in wayvnc's default us layout
		t.Fatalf("key %d, want 30 (KEY_A)", target.keys[0])
	}
}
