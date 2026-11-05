package xwm

import (
	"encoding/hex"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"bnema.dev/xturbo/xproto"
	"github.com/stretchr/testify/require"
)

// The recorded streams are the bytes an X server wrote to the XWM during a
// steady-state session of one mapped window, captured from the scripted xpeer
// (real encoded X packets: events, GetProperty replies). The scenario is
// deterministic, so a replay meets the request sequence numbers it recorded.
// They are not xtrace captures of a real Xwayland: xpeer speaks the same wire
// protocol and keeps the fixture reproducible. Regenerate with
//
//	XWM_UPDATE_STREAMS=1 go test ./internal/adapters/wayland/xwm -run TestRecordedStreams
const (
	streamCycles = 400
	streamWarm   = 20 // cycles replayed before measuring
)

const streamWin = 0x500001

type streamKind struct {
	name string
	// perCycle is Events+Replies the XWM sees per cycle.
	perCycle int
	cycle    func(hs *harness)
}

var streamKinds = []streamKind{
	// A title notification the XWM answers with one GetProperty whose reply
	// equals the stored title.
	{"property_unchanged", 2, func(hs *harness) {
		before := hs.x.Stats().Replies
		hs.p.propertyNotify(streamWin, hs.p.atom("_NET_WM_NAME"), false)
		readUntil(hs.t, hs.x, func() bool { return hs.x.Stats().Replies > before })
	}},
	// The same plus ConfigureNotify, FocusIn and MapNotify.
	{"steady", 5, func(hs *harness) {
		before := hs.x.Stats().Replies
		hs.p.propertyNotify(streamWin, hs.p.atom("_NET_WM_NAME"), false)
		readUntil(hs.t, hs.x, func() bool { return hs.x.Stats().Replies > before })
		events := hs.x.Stats().Events
		hs.p.configureNotify(streamWin)
		hs.p.focusIn(streamWin)
		hs.p.mapNotify(streamWin)
		readUntil(hs.t, hs.x, func() bool { return hs.x.Stats().Events >= events+3 })
	}},
}

// streamScenario boots, maps one window and parks the peer behind a poke so
// nothing but the test writes to the XWM afterwards (an allocation count is
// process-wide). The poke is part of the scenario, recorded or replayed.
func streamScenario(t *testing.T) *harness {
	t.Helper()
	hs := newHarness(t, nil)
	hs.classic(streamWin)
	s := hs.mapAndWait(streamWin)
	// This fixture starts after the initial MapNotify: subsequent ones are
	// steady-state duplicates, not the focus recovery transition.
	hs.x.tab.rec(s).mapped = true
	// The steady state is a focused window: a FocusIn on it is not denied,
	// so no request depends on how events are batched into flushes. The
	// poke makes sure the focus requests are out before the peer pauses.
	hs.x.Focus(hs.x.tab.lookup(streamWin))
	poke(hs)
	return hs
}

func poke(hs *harness) {
	hs.t.Helper()
	n := len(hs.p.requests())
	xproto.GetInputFocus(hs.x.conn.Buf())
	hs.x.flush()
	hs.p.waitReq(n, func(r xpeerReq) bool { return r.major == opGetInputFocus })
}

func recordStream(t *testing.T, k streamKind) []byte {
	hs := streamScenario(t)
	poke(hs)
	hs.p.mu.Lock()
	hs.p.record = true
	hs.p.mu.Unlock()
	for i := 0; i < streamCycles; i++ {
		k.cycle(hs)
	}
	hs.p.mu.Lock()
	defer hs.p.mu.Unlock()
	hs.p.record = false
	return append([]byte(nil), hs.p.tap...)
}

func streamPath(k streamKind) string {
	return filepath.Join("testdata", "streams", k.name+".hex")
}

func loadStream(t *testing.T, k streamKind) []byte {
	t.Helper()
	text, err := os.ReadFile(streamPath(k))
	require.NoError(t, err)
	b, err := hex.DecodeString(strings.Join(strings.Fields(string(text)), ""))
	require.NoError(t, err)
	return b
}

func encodeHex(b []byte) string {
	var sb strings.Builder
	for len(b) > 0 {
		n := min(32, len(b))
		sb.WriteString(hex.EncodeToString(b[:n]))
		sb.WriteByte('\n')
		b = b[n:]
	}
	return sb.String()
}

func TestRecordedStreams(t *testing.T) {
	for _, k := range streamKinds {
		t.Run(k.name, func(t *testing.T) {
			got := recordStream(t, k)
			if os.Getenv("XWM_UPDATE_STREAMS") != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(streamPath(k)), 0o755))
				require.NoError(t, os.WriteFile(streamPath(k), []byte(encodeHex(got)), 0o644))
			}
			require.Equal(t, loadStream(t, k), got, "the committed stream no longer matches the protocol the XWM speaks")
		})
	}
}

// replay feeds the recorded stream and reports the mallocs of the part after
// the warm-up cycles.
func replay(t *testing.T, k streamKind) (mallocs uint64, hs *harness) {
	t.Helper()
	stream := loadStream(t, k)
	hs = streamScenario(t)
	hs.p.pause()
	// A peer blocked in its read consumes one more request before it parks
	// (this one, or an earlier one still in flight); a parked peer leaves it
	// unread. Either way the peer ends up parked, with nothing half read.
	xproto.GetInputFocus(hs.x.conn.Buf())
	hs.x.flush()
	hs.p.waitParked()
	_, err := hs.p.f.Write(stream)
	require.NoError(t, err)

	x := hs.x
	// The harness post queue grows by append, at batching-dependent moments.
	x.post = func(func()) {}
	base := x.Stats().Events + x.Stats().Replies
	drive := func(until uint64) {
		for x.Stats().Events+x.Stats().Replies < until {

			before := x.Stats().Events + x.Stats().Replies
			x.onReadable()
			if x.Stats().Events+x.Stats().Replies == before {
				waitSocket(t, x.conn.Fd(), unix.POLLIN)
			}
		}
	}
	drive(base + uint64(streamWarm*k.perCycle))
	target := base + uint64(streamCycles*k.perCycle)

	var m0, m1 runtime.MemStats
	// A GC cycle finishing inside the window allocates runtime-internal
	// objects (sudogs), which Mallocs counts: keep the collector off.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	// Like testing.AllocsPerRun: one P, so no other goroutine's runtime
	// bookkeeping (timers, netpoll) lands in the window.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	runtime.GC()
	runtime.ReadMemStats(&m0)
	drive(target)
	runtime.ReadMemStats(&m1)
	require.Equal(t, target, x.Stats().Events+x.Stats().Replies)
	return m1.Mallocs - m0.Mallocs, hs
}

func TestPropertyUnchangedAllocations(t *testing.T) {
	m, hs := replay(t, streamKinds[0])
	require.Zero(t, m, "unchanged property cycles must not allocate")
	require.Equal(t, "net title", hs.x.tab.rec(hs.x.tab.lookup(streamWin)).title)
}

func TestDispatchAllocations(t *testing.T) {
	m, hs := replay(t, streamKinds[1])
	require.Zero(t, m, "steady-state event dispatch must not allocate")
	require.True(t, hs.x.tab.rec(hs.x.tab.lookup(streamWin)).mapped)
}
