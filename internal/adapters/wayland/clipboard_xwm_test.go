package wayland

import (
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func onDisplay(t *testing.T, s *Server, f func()) {
	t.Helper()
	if !s.display.Do(f) {
		t.Fatal("display stopped")
	}
}

// receiveVia asks the offer for mime into a fresh pipe and returns its read end.
func receiveVia(t *testing.T, cc *clipClient, offer uint32, mime string) int {
	t.Helper()
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fds[0]) })
	if err := wireRequest(cc.c, offer, uint16(wayland.DataOfferRequestReceive), []int{fds[1]}, mime); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fds[1])
	if err := cc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return fds[0]
}

// An X selection reaches the focused client and not the others, is replaced
// when a Wayland client copies (and is then not read back from X), and is
// never read while the session is protected.
func TestXSelectionReachesWaylandClients(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	state := installSecurity(t, s)
	xm := newMockxwmSelection(t)
	onDisplay(t, s, func() { s.xwmSel = xm })
	a := newClipClient(t, s, dir, events)
	b := newClipClient(t, s, dir, events)
	a.focus(t, commands)

	// X owns CLIPBOARD: the focused client gets an offer, the other none.
	xm.EXPECT().WaylandSelection(selClipboard, []string(nil)).Maybe()
	onDisplay(t, s, func() { s.setXSelection(selClipboard, []string{"text/plain;charset=utf-8", "UTF8_STRING"}) })
	offer, ok := a.selection(t)
	if !ok || offer == 0 {
		t.Fatalf("focused client got no X offer: %d %v", offer, ok)
	}
	if id, ok := b.selection(t); ok {
		t.Fatalf("unfocused client got %d", id)
	}

	// Reading reaches the XWM with a pipe it owns.
	got := make(chan string, 1)
	xm.EXPECT().SelectionReceive(selClipboard, "UTF8_STRING", mock.Anything).Run(func(_ int, mime string, fd int) {
		_, _ = unix.Write(fd, []byte("from x"))
		_ = unix.Close(fd)
		got <- mime
	}).Once()
	r := receiveVia(t, a, offer, "UTF8_STRING")
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("XWM never asked to receive")
	}
	buf := make([]byte, 16)
	n, err := unix.Read(r, buf)
	if err != nil || string(buf[:n]) != "from x" {
		t.Fatalf("pipe: %q %v", buf[:n], err)
	}

	// Focus follows: b gets the X offer too (focus replay).
	if id := b.focus(t, commands); id == 0 {
		t.Fatal("no X offer on focus")
	}

	// A Wayland copy replaces it and is exposed to X.
	xm.EXPECT().WaylandSelection(selClipboard, []string{"text/plain"}).Once()
	b.copy(t)
	if id, ok := b.selection(t); !ok || id == 0 {
		t.Fatal("copy gave no offer")
	}
	// X clearing afterwards never clobbers the Wayland owner.
	onDisplay(t, s, func() { s.setXSelection(selClipboard, nil) })
	if id, ok := b.selection(t); ok && id == 0 {
		t.Fatal("the Wayland selection was cleared by X")
	}

	// Protection: the X offer is not readable, and X cannot read Wayland.
	onDisplay(t, s, func() { s.setXSelection(selClipboard, []string{"UTF8_STRING"}) })
	protect(t, s, state, 1, true)
	var fd int
	onDisplay(t, s, func() { fd = s.xSelectionRead(selClipboard, "text/plain") })
	if fd >= 0 {
		unix.Close(fd)
		t.Fatal("protected selection readable by X")
	}
	protect(t, s, state, 2, false)
}

// A Wayland client's text selection is read for X through a pipe it fills.
func TestXSelectionReadFromWayland(t *testing.T) {
	s, events, commands, dir := keyboardServer(t)
	xm := newMockxwmSelection(t)
	onDisplay(t, s, func() { s.xwmSel = xm })
	a := newClipClient(t, s, dir, events)
	a.focus(t, commands)
	xm.EXPECT().WaylandSelection(selClipboard, []string(nil)).Maybe() // teardown destroys the source
	xm.EXPECT().WaylandSelection(selClipboard, []string{"text/plain"}).Once()
	src := a.copy(t)
	var fd int
	onDisplay(t, s, func() { fd = s.xSelectionRead(selClipboard, "text/plain") })
	if fd < 0 {
		t.Fatal("no read end")
	}
	defer unix.Close(fd)
	deadline := time.After(2 * time.Second)
	for len(src.sends) == 0 {
		if err := a.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-deadline:
			t.Fatal("source never asked to send")
		case <-time.After(5 * time.Millisecond):
		}
	}
	buf := make([]byte, 16)
	n, err := unix.Read(fd, buf)
	if err != nil || string(buf[:n]) != "pasted" {
		t.Fatalf("read %q %v", buf[:n], err)
	}
	onDisplay(t, s, func() {
		fd2 := s.xSelectionRead(selClipboard, "image/png")
		if fd2 >= 0 {
			unix.Close(fd2)
			t.Error("unoffered mime readable")
		}
	})
}
