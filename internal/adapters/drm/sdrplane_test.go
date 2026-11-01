package drm

import (
	"bytes"
	"encoding/binary"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/domain"
)

// Golden bytes: row-major 3x4, little-endian S31.32 sign-magnitude, zero offset column.
func TestCTMBlobGolden(t *testing.T) {
	b := ctmBytes([3][3]float64{{1, -0.5, 0}, {0, 1, 0}, {0, 0, 1}})
	want := []uint64{1 << 32, 1<<63 | 1<<31, 0, 0, 0, 1 << 32, 0, 0, 0, 0, 1 << 32, 0}
	if len(b) != len(want)*8 {
		t.Fatalf("blob length %d", len(b))
	}
	for i, w := range want {
		if g := binary.LittleEndian.Uint64(b[i*8:]); g != w {
			t.Errorf("word %d = %#x, want %#x", i, g, w)
		}
	}
}

func TestSDRPlaneOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("NEFERWL_DRM_SDR_PLANE", value)
			mode, why := planeColor(domain.SurfaceColor{}, fourccXRGB, true, true, &colorPipeline{})
			if value == "1" {
				if mode != colorSDRToPQ || why != "" {
					t.Fatalf("got %d %q", mode, why)
				}
			} else if mode != colorBypass || why != "hdr_sdr_content" {
				t.Fatalf("got %d %q", mode, why)
			}
			pq := domain.SurfaceColor{TF: domain.ColorTFPQ, Primaries: domain.ColorPrimariesBT2020}
			if mode, why := planeColor(pq, fourccXR30, true, true, nil); mode != colorBypass || why != "" {
				t.Fatalf("PQ: %d %q", mode, why)
			}
			if mode, why := planeColor(domain.SurfaceColor{}, fourccXRGB, true, false, nil); mode != colorBypass || why != "" {
				t.Fatalf("SDR output: %d %q", mode, why)
			}
		})
	}
}

func TestSDRPlaneGateDecisionAndTransitionLog(t *testing.T) {
	o, _, _ := colorOutput(t, false, nil)
	t.Setenv("NEFERWL_DRM_SDR_PLANE", "")
	var log bytes.Buffer
	o.renderLog = zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &log}).WithField("component", "render")
	c := sdrContent(o)
	c.LogicalW, c.LogicalH = 200, 100
	s := domain.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []domain.SceneWindow{{ID: 1, Fullscreen: true, Rect: domain.Rect{W: 200, H: 100}}}}
	surfaces := map[domain.WindowID]domain.SurfaceContent{1: c}
	for range 3 {
		d := o.decideFrame(s, surfaces, false)
		if d.fb != 0 || d.overlay.fb != 0 || o.reason != "hdr_sdr_content" {
			t.Fatalf("decision=%+v reason=%q", d, o.reason)
		}
	}
	if n := bytes.Count(log.Bytes(), []byte(`"reason":"hdr_sdr_content"`)); n != 1 {
		t.Fatalf("got %d refusal logs: %s", n, log.String())
	}
	t.Setenv("NEFERWL_DRM_SDR_PLANE", "1")
	if d := o.decideFrame(s, surfaces, false); d.fb != 80 || d.color != colorSDRToPQ {
		t.Fatalf("opt-in decision=%+v", d)
	}
	t.Setenv("NEFERWL_DRM_SDR_PLANE", "")
	o.decideFrame(s, surfaces, false)
	if n := bytes.Count(log.Bytes(), []byte(`"reason":"hdr_sdr_content"`)); n != 2 {
		t.Fatalf("got %d refusal logs: %s", n, log.String())
	}
}

func TestSDRExpansionRefusesPlanes(t *testing.T) {
	for _, optIn := range []string{"", "1"} {
		t.Run("sdr_plane="+optIn, func(t *testing.T) {
			t.Setenv("NEFERWL_DRM_SDR_PLANE", optIn)
			o, _, _ := colorOutput(t, true, nil)
			c := sdrContent(o)
			c.LogicalW, c.LogicalH = 200, 100
			s := domain.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []domain.SceneWindow{{ID: 1, Fullscreen: true, Rect: domain.Rect{W: 200, H: 100}}}}
			surfaces := map[domain.WindowID]domain.SurfaceContent{1: c}
			for _, intensity := range []float64{0, 0.5} {
				s.Windows[0].SDRExpansion = (domain.SDRExpansion{Intensity: intensity, PeakNits: 604, SDRWhiteNits: 203}).Curve()
				d := o.decideFrame(s, surfaces, false)
				if intensity > 0 {
					if d.fb != 0 || d.overlay.fb != 0 || o.reason != "sdr_expansion" || o.overlayReason != "sdr_expansion" {
						t.Fatalf("expanded decision=%+v reasons=%q/%q", d, o.reason, o.overlayReason)
					}
				} else if optIn == "1" && (d.fb != 80 || d.color != colorSDRToPQ) {
					t.Fatalf("inactive expansion prevented scanout: %+v", d)
				}
			}
		})
	}
}
