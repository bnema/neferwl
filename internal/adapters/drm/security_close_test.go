package drm

import (
	"maps"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestInactiveOnCloseRequiresSuccessfulValidatedDisable(t *testing.T) {
	for _, stage := range []string{"success", "commit-failure", "query-failure", "partial-primary", "partial-cursor", "partial-overlay", "partial-stray", "missing-active"} {
		t.Run(stage, func(t *testing.T) {
			var failures []error
			if stage == "commit-failure" {
				failures = []error{unix.EACCES}
			}
			o, k, commits := testOutput(t, failures...)
			var state atomic.Uint64
			securityGate(t, o, &state) // production gate, even unlocked: disable
			o.off = true               // stale cached state is never terminal evidence
			o.inactiveOnClose = true   // reset any earlier result at entry
			o.saved = modeCrtc{crtcID: tCrtc, fbID: 123, modeValid: 1, mode: o.mode}
			o.overlay = &plane{id: tOverlay, props: planeProps}
			stray := &plane{id: 53, props: planeProps}
			o.stray = []*plane{o.overlay, stray}
			var readErr error
			if stage == "query-failure" {
				readErr = unix.EACCES
			}
			k.EXPECT().objProps(uint32(53), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, tCrtc}}, readErr).Once()
			partial := func(p *plane) { p.props = maps.Clone(planeProps); p.props["FB_ID"] = 0 }
			switch stage {
			case "partial-primary":
				partial(o.primary)
			case "partial-cursor":
				partial(o.cursor.plane)
			case "partial-overlay":
				partial(o.overlay)
			case "partial-stray":
				partial(stray)
			case "missing-active":
				o.crtcProps = maps.Clone(o.crtcProps)
				o.crtcProps["ACTIVE"] = 0
			}
			k.EXPECT().rmFB(mock.Anything).Return(nil)
			done := make(chan struct{})
			go func() { o.Close(); close(done) }()
			<-done // accessor is only read after owner-completion synchronization
			if got := o.InactiveOnClose(); got != (stage == "success") {
				t.Fatalf("inactiveOnClose=%v stage=%s", got, stage)
			}
			if len(*commits) != 1 {
				t.Fatalf("commits %d", len(*commits))
			}
			if stage == "success" {
				checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor, tOverlay, 53)
			}
		})
	}
}

func TestInactiveOnCloseSavedRestoreNeverCertifiesRemoval(t *testing.T) {
	o, k, _ := testOutput(t)
	o.saved = modeCrtc{crtcID: tCrtc, fbID: 123, modeValid: 1, mode: o.mode}
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
	k.EXPECT().destroyBlob(uint32(99)).Return(nil).Once()
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	o.Close()
	if o.InactiveOnClose() {
		t.Fatal("legacy saved desktop restoration certified inactivity")
	}
}
