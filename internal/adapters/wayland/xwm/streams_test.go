package xwm

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	hs.mapAndWait(streamWin)
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
	poke(hs)
	_, err := hs.p.f.Write(stream)
	require.NoError(t, err)

	x := hs.x
	base := x.Stats().Events + x.Stats().Replies
	drive := func(until uint64) {
		deadline := time.Now().Add(10 * time.Second)
		for x.Stats().Events+x.Stats().Replies < until {
			if time.Now().After(deadline) { // no formatting arguments: boxing them would count as mallocs
				t.Fatal("stream stalled")
			}
			before := x.Stats().Events + x.Stats().Replies
			x.onReadable()
			if x.Stats().Events+x.Stats().Replies == before {
				runtime.Gosched()
			}
		}
	}
	drive(base + uint64(streamWarm*k.perCycle))
	target := base + uint64(streamCycles*k.perCycle)

	var m0, m1 runtime.MemStats
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
	require.Equal(t, "net title", hs.x.tab.rec(hs.x.Slot(streamWin)).title)
}

func TestDispatchAllocations(t *testing.T) {
	m, hs := replay(t, streamKinds[1])
	require.Zero(t, m, "steady-state event dispatch must not allocate")
	require.True(t, hs.x.tab.rec(hs.x.Slot(streamWin)).mapped)
}
