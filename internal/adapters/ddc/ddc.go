// Package ddc asks monitors over DDC/CI which input source they show, so
// core knows when a connected display shows another computer.
package ddc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// openRetry is the time between two tries to open the DDC bus.
	openRetry = 30 * time.Second

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

// Watch reports the input source the monitor on connector (e.g.
// "HDMI-A-1") of card (e.g. "/dev/dri/card1") shows, as ports.OutputInput
// through send, until ctx ends or send returns false. A value is reported
// once stableReads reads in a row agree; a monitor that stops answering is
// reported as 0 (unknown). While the bus cannot be opened (i2c-dev not
// loaded, no access), Watch retries every openRetry. log carries the "ddc"
// component.
func Watch(ctx context.Context, card, connector string, clock ports.Clock, send func(ports.OutputInput) bool, log zerowrap.Logger) {
	retry := clock.NewTimer(openRetry)
	defer retry.Stop()
	for logged := false; ; logged = true {
		b, err := openBus(card, connector)
		if err == nil {
			defer b.close()
			w := &watcher{name: connector, bus: b, clock: clock, send: send, log: log}
			w.run(ctx)
			return
		}
		if !logged {
			log.Info().Str("connector", connector).Err(err).Msg("input source detection off")
		}
		retry.Reset(openRetry)
		select {
		case <-ctx.Done():
			return
		case <-retry.C():
		}
	}
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
	name  string
	bus   bus
	clock ports.Clock
	send  func(ports.OutputInput) bool
	log   zerowrap.Logger
	// candidate is the latest input read (0: no answer) and agreed the
	// number of reads in a row that found it; sent is the last reported.
	candidate, sent uint16
	agreed          int
}

func (w *watcher) run(ctx context.Context) {
	tick := w.clock.NewTicker(Interval)
	defer tick.Stop()
	delay := w.clock.NewTimer(replyDelay)
	delay.Stop()
	defer delay.Stop()
	for {
		src, err := w.input(ctx, delay)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// Asleep, off or busy: 0 once it lasts.
			w.log.Debug().Str("connector", w.name).Err(err).Msg("input source read")
			src = 0
		}
		if !w.observe(src) {
			return
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

// observe reports src once stableReads reads in a row found it and it
// changed. It returns false when send gives up.
func (w *watcher) observe(src uint16) bool {
	if src != w.candidate {
		w.candidate, w.agreed = src, 0
	}
	w.agreed++
	if w.agreed < stableReads || src == w.sent {
		return true
	}
	w.log.Info().Str("connector", w.name).Str("input", ports.InputName(src)).Msg("input source")
	if !w.send(ports.OutputInput{Name: w.name, Input: src}) {
		return false
	}
	w.sent = src
	return true
}
