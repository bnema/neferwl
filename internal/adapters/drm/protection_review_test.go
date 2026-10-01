package drm

import (
	"context"
	"errors"
	"maps"
	"os"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestProtectedModesetPartialPlaneMetadataFailsClosed(t *testing.T) {
	for _, role := range []string{"primary", "cursor", "overlay", "stray"} {
		for _, missing := range []string{"FB_ID", "CRTC_ID"} {
			t.Run(role+"/"+missing, func(t *testing.T) {
				o, k, commits := testOutput(t)
				props := maps.Clone(planeProps)
				props[missing] = 0
				p := &plane{id: tOverlay, props: props, crtc: tCrtc}
				switch role {
				case "primary":
					o.primary.props = props
				case "cursor":
					o.cursor.plane.props = props
				case "overlay":
					o.overlay = p
					o.stray = []*plane{p} // pickOverlay keeps the plane in stray.
				case "stray":
					o.stray = []*plane{p} // old master's overlay may still be here.
					k.EXPECT().objProps(uint32(tOverlay), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, tCrtc}}, nil).Once()
				}
				r := portsmocks.NewMockRenderer(t) // no GPU calls permitted
				if err := o.protectedModeset(context.Background(), r); err == nil {
					t.Fatal("partial metadata reported protection success")
				}
				if !o.protected || len(*commits) != 0 || o.off || o.back != 0 {
					t.Fatal("unverified detachment committed, flipped or claimed off")
				}
			})
		}
	}
}

func TestProtectedModesetSkipsOwnedStrayDuplicates(t *testing.T) {
	o, k, commits := testOutput(t)
	o.overlay = &plane{id: tOverlay, props: planeProps}
	o.stray = []*plane{o.overlay, o.primary, o.cursor.plane}
	// Even if a fresh query would fail, cached owned-plane IDs suffice.
	// No owned plane may be queried, during disable or activation.
	k.EXPECT().objProps(mock.Anything, uint32(objPlane)).Return(nil, unix.EACCES).Maybe()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	if err := o.protectedModeset(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	k.AssertNotCalled(t, "objProps", mock.Anything, mock.Anything)
	if len(*commits) != 2 {
		t.Fatalf("commits %d", len(*commits))
	}
	checkProtectedDisable(t, (*commits)[0], tPrimary, tCursor, tOverlay)
}

func TestProtectedModesetDisableSendsInactiveFormats(t *testing.T) {
	for _, stage := range []string{"disable-failure", "clear-failure", "cancel", "off", "activate"} {
		t.Run(stage, func(t *testing.T) {
			var failures []error
			if stage == "disable-failure" {
				failures = []error{unix.EACCES}
			}
			o, k, _ := testOutput(t, failures...)
			o.device, o.hdr.on = 42, true
			feedback := make(chan ports.OutputFormats, 2)
			o.formats = feedback
			r := portsmocks.NewMockRenderer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage != "disable-failure" {
				r.EXPECT().UseTarget(0).Return().Once()
				r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
					// Disable feedback must precede GPU writes, not just return.
					select {
					case f := <-feedback:
						if f.Output != o.conn.name || f.Device != 42 || len(f.Formats) != 0 || f.HDR != nil || !o.off {
							t.Fatalf("not inactive feedback: %+v off=%v", f, o.off)
						}
					default:
						t.Fatal("successful disable sent no inactive feedback")
					}
					if stage == "clear-failure" {
						return nil, errors.New("clear failed")
					}
					if stage == "cancel" {
						cancel()
					}
					return nil, nil
				}).Once()
			}
			if stage == "off" {
				o.wantOff = true
			}
			if stage == "activate" {
				k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
			}
			err := o.protectedModeset(ctx, r)
			wantErr := stage == "disable-failure" || stage == "clear-failure" || stage == "cancel"
			if (err != nil) != wantErr {
				t.Fatalf("result %v", err)
			}
			if stage == "activate" {
				select {
				case f := <-feedback:
					if f.HDR == nil || o.off {
						t.Fatalf("activation did not send active feedback: %+v", f)
					}
				default:
					t.Fatal("missing active feedback after activation")
				}
			}
			if len(feedback) != 0 {
				t.Fatal("unexpected feedback without a successful state transition")
			}
		})
	}
}
