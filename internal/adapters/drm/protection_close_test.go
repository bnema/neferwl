package drm

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestProtectedCloseUnreadableStrayStillDisablesWithoutRestore(t *testing.T) {
	o, k, commits := testOutput(t)
	o.protected = true
	o.saved = modeCrtc{crtcID: tCrtc, fbID: 123, modeValid: 1, mode: o.mode}
	o.stray = []*plane{{id: 53, props: planeProps}, {id: 54, props: planeProps}}
	k.EXPECT().objProps(uint32(53), uint32(objPlane)).Return(nil, unix.EACCES).Once()
	k.EXPECT().objProps(uint32(54), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, tCrtc}}, nil).Once()
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	o.Close() // best effort, no readiness or protection ACK
	if len(*commits) != 1 || !o.protected {
		t.Fatal("query failure skipped disable or dropped protection")
	}
	checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor, 54)
	if _, ok := (*commits)[0].req.value(53, pFB); ok {
		t.Fatal("claimed ownership of unreadable stray")
	}
}
