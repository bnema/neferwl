package libinput

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

const (
	testPad      uintptr = 1
	testMouse    uintptr = 2
	testKeyboard uintptr = 3
)

// Every shared field differs between the two, so a mixed-up route fails.
var (
	padCfg   = ports.TouchpadConfig{PointerConfig: ports.PointerConfig{NaturalScroll: true, AccelSpeed: 0.5, AccelProfile: ports.AccelFlat}, Tap: true}
	mouseCfg = ports.PointerConfig{AccelSpeed: -0.25, AccelProfile: ports.AccelAdaptive, LeftHanded: true}
)

// newState returns an input state over a device config mock, and a logger
// writing to the returned buffer.
func newState(t *testing.T) (*inputState, *mockdeviceConfig, zerowrap.Logger, *bytes.Buffer) {
	t.Helper()
	dev := newMockdeviceConfig(t)
	buf := &bytes.Buffer{}
	log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: buf}).WithField("component", "input")
	return &inputState{cfg: ports.InputDevicesConfig{Touchpad: padCfg, Mouse: mouseCfg}, dev: dev}, dev, log, buf
}

func TestConfigure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dev    uintptr
		expect func(m *mockdeviceConfig)
	}{
		{"touchpad", testPad, func(m *mockdeviceConfig) {
			m.EXPECT().IsTouchpad(testPad).Return(true)
			m.EXPECT().SetNaturalScroll(testPad, true).Return(nil).Once()
			m.EXPECT().SetAccel(testPad, 0.5, ports.AccelFlat).Return(nil).Once()
			m.EXPECT().SetLeftHanded(testPad, false).Return(nil).Once()
			m.EXPECT().SetTap(testPad, true).Return(nil).Once()
		}},
		// The touchpad settings must not reach the mouse, and a mouse is
		// never given tapping: SetTap has no expectation.
		{"mouse", testMouse, func(m *mockdeviceConfig) {
			m.EXPECT().IsTouchpad(testMouse).Return(false)
			m.EXPECT().IsPointer(testMouse).Return(true)
			m.EXPECT().SetNaturalScroll(testMouse, false).Return(nil).Once()
			m.EXPECT().SetAccel(testMouse, -0.25, ports.AccelAdaptive).Return(nil).Once()
			m.EXPECT().SetLeftHanded(testMouse, true).Return(nil).Once()
		}},
		{"keyboard", testKeyboard, func(m *mockdeviceConfig) {
			m.EXPECT().IsTouchpad(testKeyboard).Return(false)
			m.EXPECT().IsPointer(testKeyboard).Return(false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, dev, log, buf := newState(t)
			tc.expect(dev)
			in.configure(tc.dev, log)
			if buf.Len() != 0 {
				t.Fatalf("logged: %s", buf)
			}
		})
	}
}

func TestConfigurable(t *testing.T) {
	for _, tc := range []struct {
		name           string
		touchpad, ptr  bool
		want, askedPtr bool
	}{
		{"touchpad", true, false, true, false},
		{"mouse", false, true, true, true},
		{"keyboard", false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, dev, _, _ := newState(t)
			dev.EXPECT().IsTouchpad(testPad).Return(tc.touchpad)
			if tc.askedPtr {
				dev.EXPECT().IsPointer(testPad).Return(tc.ptr)
			}
			if got := in.configurable(testPad); got != tc.want {
				t.Fatalf("configurable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfigureRejectedSettingLogsAndContinues(t *testing.T) {
	in, dev, log, buf := newState(t)
	dev.EXPECT().IsTouchpad(testPad).Return(true)
	dev.EXPECT().SetNaturalScroll(testPad, true).Return(errRejected).Once()
	dev.EXPECT().Name(testPad).Return("Synaptics").Once()
	dev.EXPECT().SetAccel(testPad, 0.5, ports.AccelFlat).Return(nil).Once()
	dev.EXPECT().SetLeftHanded(testPad, false).Return(nil).Once()
	dev.EXPECT().SetTap(testPad, true).Return(nil).Once()
	in.configure(testPad, log)
	out := buf.String()
	for _, want := range []string{`"device":"Synaptics"`, `"setting":`, `"component":"input"`, `"level":"warn"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log %s lacks %s", out, want)
		}
	}
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("%d log lines, want 1: %s", n, out)
	}
}
