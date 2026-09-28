// Package ddc asks monitors over DDC/CI which input source they show, so
// core knows when a connected display shows another computer.
package ddc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

const (
	// Interval is the time between two input source reads.
	Interval = 2 * time.Second
	// replyDelay is the DDC/CI wait between a request and its reply.
	replyDelay = 50 * time.Millisecond
	// stableReads is how many reads in a row must agree before core hears
	// of a change: one lost read never moves workspaces.
	stableReads = 2

	addrDDC     = 0x37
	vcpInput    = 0x60
	hostAddr    = 0x51
	displayAddr = 0x6e
)

// bus is the I2C bus of one connector.
type bus interface {
	write(addr uint16, b []byte) error
	read(addr uint16, b []byte) error
	close() error
}

// Watch reports whether the monitor on connector (e.g. "HDMI-A-1") of card
// (e.g. "/dev/dri/card1") shows this computer, as ports.OutputShown on
// events, until ctx ends. A monitor that never answers DDC/CI is never
// reported: it counts as shown. log carries the "ddc" component.
func Watch(ctx context.Context, card, connector string, clock ports.Clock, events chan<- ports.OutputEvent, log zerowrap.Logger) {
	b, err := openBus(card, connector)
	if err != nil {
		log.Info().Str("connector", connector).Err(err).Msg("input source detection off")
		return
	}
	defer b.close()
	w := &watcher{name: connector, kind: connectorKind(connector), bus: b, clock: clock, events: events, log: log}
	w.run(ctx)
}

// openBus opens the connector's DDC bus, /dev/i2c-N from sysfs.
func openBus(card, connector string) (bus, error) {
	link, err := filepath.EvalSymlinks(filepath.Join("/sys/class/drm", filepath.Base(card)+"-"+connector, "ddc"))
	if err != nil {
		return nil, fmt.Errorf("no DDC bus: %w", err)
	}
	b, err := openI2C("/dev/" + filepath.Base(link))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("load the i2c-dev module: %w", err)
	case err != nil:
		return nil, err
	}
	return b, nil
}

type watcher struct {
	name   string
	kind   string // "hdmi", "dp" or "other"
	bus    bus
	clock  ports.Clock
	events chan<- ports.OutputEvent
	log    zerowrap.Logger
	// ours is the input source showing this computer, 0 until known.
	ours uint16
	// candidate is the latest state read and agreed reads the number of
	// reads in a row that found it; sent is the last state reported.
	candidate, sent, reported bool
	agreed                    int
}

func (w *watcher) run(ctx context.Context) {
	tick := w.clock.NewTicker(Interval)
	defer tick.Stop()
	delay := w.clock.NewTimer(replyDelay)
	delay.Stop()
	defer delay.Stop()
	for {
		src, err := w.input(ctx, delay)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			// Asleep, off or busy: no answer tells nothing.
			w.log.Debug().Str("connector", w.name).Err(err).Msg("input source read")
		default:
			if !w.observe(ctx, src) {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C():
		}
	}
}

// input reads VCP 0x60, the input source the monitor shows.
func (w *watcher) input(ctx context.Context, delay ports.Timer) (uint16, error) {
	req := []byte{hostAddr, 0x82, 0x01, vcpInput, 0}
	req[4] = displayAddr ^ req[0] ^ req[1] ^ req[2] ^ req[3]
	if err := w.bus.write(addrDDC, req); err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	delay.Reset(replyDelay)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-delay.C():
	}
	r := make([]byte, 11)
	if err := w.bus.read(addrDDC, r); err != nil {
		return 0, fmt.Errorf("reply: %w", err)
	}
	return parseReply(r)
}

// parseReply decodes a VCP feature reply:
// 6E 88 02 <result> 60 <type> <max hi> <max lo> <cur hi> <cur lo> <checksum>.
func parseReply(r []byte) (uint16, error) {
	chk := byte(0x50)
	for _, c := range r[:10] {
		chk ^= c
	}
	switch {
	case r[1]&0x7f == 0:
		return 0, errors.New("null reply")
	case r[0] != displayAddr || r[1] != 0x88 || r[2] != 0x02 || r[4] != vcpInput || chk != r[10]:
		return 0, fmt.Errorf("bad reply % x", r)
	case r[3] != 0:
		return 0, errors.New("input source not supported")
	}
	return uint16(r[8])<<8 | uint16(r[9]), nil
}

// observe classifies src and reports a state read stableReads times in a
// row. It returns false when ctx ends.
func (w *watcher) observe(ctx context.Context, src uint16) bool {
	shown := w.shows(src)
	if shown != w.candidate || w.agreed == 0 {
		w.candidate, w.agreed = shown, 0
	}
	w.agreed++
	if w.agreed < stableReads || (w.reported && w.sent == shown) {
		return true
	}
	w.log.Info().Str("connector", w.name).Bool("shown", shown).Uint16("input", src).Msg("input source")
	select {
	case w.events <- ports.OutputShown{Name: w.name, Shown: shown}:
		w.sent, w.reported = shown, true
		return true
	case <-ctx.Done():
		return false
	}
}

// shows reports whether src is this computer's input. The first source of
// the connector's kind (HDMI for HDMI-A-1) is ours; one of another kind
// never is. Vendor codes of no known kind are taken as they come. DDC/CI
// does not tell which port the cable is on: a monitor started on another
// input of the same kind (HDMI-1 for a cable in HDMI-2) is taken as ours.
func (w *watcher) shows(src uint16) bool {
	if w.ours != 0 {
		return src == w.ours
	}
	if k := sourceKind(src); k != "" && k != w.kind {
		return false
	}
	w.ours = src
	return true
}

// sourceKind is the kind of an MCCS input source value.
func sourceKind(src uint16) string {
	switch src & 0xff {
	case 0x0f, 0x10:
		return "dp"
	case 0x11, 0x12:
		return "hdmi"
	case 0x01, 0x02, 0x03, 0x04:
		return "other"
	}
	return ""
}

// connectorKind is the kind of a DRM connector name.
func connectorKind(name string) string {
	switch {
	case strings.HasPrefix(name, "HDMI-"):
		return "hdmi"
	case strings.HasPrefix(name, "DP-"):
		return "dp"
	}
	return "other"
}
