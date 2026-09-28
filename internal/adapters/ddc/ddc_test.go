package ddc

import (
	"context"
	"errors"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
)

// reply is a VCP 0x60 reply with src as the current input source.
func reply(src uint16) []byte {
	r := []byte{displayAddr, 0x88, 0x02, 0x00, vcpInput, 0x00, 0x00, 0x1b, byte(src >> 8), byte(src), 0}
	chk := byte(0x50)
	for _, c := range r[:10] {
		chk ^= c
	}
	r[10] = chk
	return r
}

// rig runs a watcher on connector whose bus answers each read with the
// next entry of answers (a reply or an error). The test drives the reads:
// tick starts the next one. A last failed read follows the answers: once
// it happens, the watcher has handled every answer.
type rig struct {
	events chan ports.OutputEvent
	tick   chan time.Time
	reads  chan struct{}
	stop   func()
}

func startRig(t *testing.T, connector string, answers ...any) *rig {
	t.Helper()
	answers = append(answers, errors.New("end"))
	r := &rig{events: make(chan ports.OutputEvent, 8), tick: make(chan time.Time), reads: make(chan struct{}, len(answers)+1)}
	b := newMockbus(t)
	want := []byte{hostAddr, 0x82, 0x01, vcpInput, displayAddr ^ hostAddr ^ 0x82 ^ 0x01 ^ vcpInput}
	b.EXPECT().write(uint16(addrDDC), want).Return(nil).Times(len(answers))
	for _, a := range answers {
		b.EXPECT().read(uint16(addrDDC), mock.Anything).RunAndReturn(func(_ uint16, buf []byte) error {
			defer func() { r.reads <- struct{}{} }()
			if err, ok := a.(error); ok {
				return err
			}
			copy(buf, a.([]byte))
			return nil
		}).Once()
	}
	ticker := portsmocks.NewMockTicker(t)
	ticker.EXPECT().C().Return(r.tick)
	ticker.EXPECT().Stop().Return()
	fired := make(chan time.Time, 1)
	delay := portsmocks.NewMockTimer(t)
	delay.EXPECT().Stop().Return(true)
	delay.EXPECT().Reset(replyDelay).RunAndReturn(func(time.Duration) bool { fired <- time.Time{}; return false })
	delay.EXPECT().C().Return(fired)
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().NewTicker(Interval).Return(ticker).Once()
	clock.EXPECT().NewTimer(replyDelay).Return(delay).Once()
	w := &watcher{name: connector, kind: connectorKind(connector), bus: b, clock: clock, events: r.events, log: zerowrap.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	r.stop = func() { cancel(); <-done }
	t.Cleanup(r.stop)
	return r
}

// next waits for the current read, then starts the following one.
func (r *rig) next(t *testing.T) {
	t.Helper()
	select {
	case <-r.reads:
	case <-time.After(2 * time.Second):
		t.Fatal("no read")
	}
	r.tick <- time.Time{}
}

// last waits until every answer is handled and stops the watcher.
func (r *rig) last(t *testing.T) {
	t.Helper()
	r.next(t)
	select {
	case <-r.reads:
	case <-time.After(2 * time.Second):
		t.Fatal("no read")
	}
	r.stop()
}

func (r *rig) got() []ports.OutputShown {
	var list []ports.OutputShown
	for len(r.events) > 0 {
		list = append(list, (<-r.events).(ports.OutputShown))
	}
	return list
}

func expect(t *testing.T, got []ports.OutputShown, want ...ports.OutputShown) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

const (
	dp2   = 0x10
	hdmi2 = 0x12
)

// The monitor switching to another input and back is reported once each
// way, after two reads agree.
func TestInputSourceChange(t *testing.T) {
	r := startRig(t, "HDMI-A-1", reply(hdmi2), reply(hdmi2), reply(dp2), reply(dp2), reply(dp2), reply(hdmi2), reply(hdmi2))
	for range 6 {
		r.next(t)
	}
	r.last(t)
	expect(t, r.got(),
		ports.OutputShown{Name: "HDMI-A-1", Shown: true},
		ports.OutputShown{Name: "HDMI-A-1", Shown: false},
		ports.OutputShown{Name: "HDMI-A-1", Shown: true})
}

// One odd read between two agreeing ones moves nothing; failed reads
// neither count nor reset the agreement.
func TestInputSourceNeedsTwoReads(t *testing.T) {
	busy := errors.New("remote I/O error")
	r := startRig(t, "HDMI-A-1", reply(hdmi2), reply(hdmi2), reply(dp2), reply(hdmi2), busy, reply(0x00), reply(dp2), busy, reply(dp2))
	for range 8 {
		r.next(t)
	}
	r.last(t)
	expect(t, r.got(),
		ports.OutputShown{Name: "HDMI-A-1", Shown: true},
		ports.OutputShown{Name: "HDMI-A-1", Shown: false})
}

// Started while the monitor shows a DisplayPort input, an HDMI connector
// is hidden; the first HDMI input then becomes ours.
func TestInputSourceStartsOnAnotherInput(t *testing.T) {
	r := startRig(t, "HDMI-A-1", reply(dp2), reply(dp2), reply(hdmi2), reply(hdmi2))
	for range 3 {
		r.next(t)
	}
	r.last(t)
	expect(t, r.got(),
		ports.OutputShown{Name: "HDMI-A-1", Shown: false},
		ports.OutputShown{Name: "HDMI-A-1", Shown: true})
}

// A monitor that never answers reports nothing: it counts as shown.
func TestInputSourceNoAnswer(t *testing.T) {
	r := startRig(t, "DP-2", errors.New("no device"), errors.New("no device"), errors.New("no device"))
	r.next(t)
	r.next(t)
	r.last(t)
	expect(t, r.got())
}

func TestParseReply(t *testing.T) {
	if src, err := parseReply(reply(hdmi2)); err != nil || src != hdmi2 {
		t.Fatal(src, err)
	}
	bad := reply(hdmi2)
	bad[10] ^= 1
	unsupported := reply(hdmi2)
	unsupported[3] = 1
	unsupported[10] ^= 1
	null := []byte{displayAddr, 0x80, 0xbe, 0, 0, 0, 0, 0, 0, 0, 0}
	for name, r := range map[string][]byte{"checksum": bad, "unsupported": unsupported, "null": null} {
		if _, err := parseReply(r); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
