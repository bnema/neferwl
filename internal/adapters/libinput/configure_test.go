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

var (
	padCfg   = ports.TouchpadConfig{PointerConfig: ports.PointerConfig{NaturalScroll: true, AccelSpeed: 0.5, AccelProfile: ports.AccelFlat, LeftHanded: true}, Tap: true}
	mouseCfg = ports.PointerConfig{AccelSpeed: -0.25, AccelProfile: ports.AccelAdaptive, LeftHanded: true}
)

func newState(t *testing.T) (*inputState, *mockdeviceConfig, *bytes.Buffer) {
	t.Helper()
	dev := newMockdeviceConfig(t)
	buf := &bytes.Buffer{}
	return &inputState{cfg: ports.InputDevicesConfig{Touchpad: padCfg, Mouse: mouseCfg}, dev: dev}, dev, buf
}

func testLog(buf *bytes.Buffer) zerowrap.Logger {
	return zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: buf}).WithField("component", "input")
}

func TestConfigureTouchpad(t *testing.T) {
	in, dev, buf := newState(t)
	dev.EXPECT().IsTouchpad(testPad).Return(true)
	dev.EXPECT().SetNaturalScroll(testPad, true).Return(nil).Once()
	dev.EXPECT().SetAccel(testPad, 0.5, ports.AccelFlat).Return(nil).Once()
	dev.EXPECT().SetLeftHanded(testPad, true).Return(nil).Once()
	dev.EXPECT().SetTap(testPad, true).Return(nil).Once()
	in.configure(testPad, testLog(buf))
	if buf.Len() != 0 {
		t.Fatalf("success logged: %s", buf)
	}
}

func TestConfigureMouse(t *testing.T) {
	in, dev, buf := newState(t)
	dev.EXPECT().IsTouchpad(testMouse).Return(false)
	dev.EXPECT().IsPointer(testMouse).Return(true)
	// The touchpad natural scroll (on) must not reach the mouse (off), and a
	// mouse is never given tapping: SetTap has no expectation.
	dev.EXPECT().SetNaturalScroll(testMouse, false).Return(nil).Once()
	dev.EXPECT().SetAccel(testMouse, -0.25, ports.AccelAdaptive).Return(nil).Once()
	dev.EXPECT().SetLeftHanded(testMouse, true).Return(nil).Once()
	in.configure(testMouse, testLog(buf))
	if buf.Len() != 0 {
		t.Fatalf("success logged: %s", buf)
	}
}

func TestConfigureIgnoresOtherDevices(t *testing.T) {
	in, dev, buf := newState(t)
	dev.EXPECT().IsTouchpad(testKeyboard).Return(false)
	dev.EXPECT().IsPointer(testKeyboard).Return(false)
	in.configure(testKeyboard, testLog(buf))
	if buf.Len() != 0 {
		t.Fatalf("keyboard logged: %s", buf)
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
			in, dev, _ := newState(t)
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
	in, dev, buf := newState(t)
	dev.EXPECT().IsTouchpad(testPad).Return(true)
	dev.EXPECT().SetNaturalScroll(testPad, true).Return(errRejected).Once()
	dev.EXPECT().Name(testPad).Return("Synaptics").Once()
	dev.EXPECT().SetAccel(testPad, 0.5, ports.AccelFlat).Return(nil).Once()
	dev.EXPECT().SetLeftHanded(testPad, true).Return(nil).Once()
	dev.EXPECT().SetTap(testPad, true).Return(nil).Once()
	in.configure(testPad, testLog(buf))
	out := buf.String()
	for _, want := range []string{`"device":"Synaptics"`, `"setting":"natural scroll"`, `"component":"input"`, `"level":"warn"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log %s lacks %s", out, want)
		}
	}
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("%d log lines, want 1: %s", n, out)
	}
}

func TestLibinputDevicesDoNotAllocate(t *testing.T) {
	var d deviceConfig
	if allocs := testing.AllocsPerRun(100, func() { d = libinputDevices{} }); allocs != 0 {
		t.Fatalf("boxing libinputDevices allocates %v times", allocs)
	}
	_ = d
}
