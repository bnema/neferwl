package core_test

import (
	"regexp"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// The drag thumbnail keeps the window's SDR expansion on an HDR output.
func TestDropProxyKeepsSDRExpansion(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r := startDragMode(t, animated, func(c *domain.Config) {
			on := domain.SDRExpandOn
			c.Rules = []domain.WindowRule{{Name: "hdr", AppID: regexp.MustCompile(`^game$`), SDRExpand: &on}}
		}, dragOut, domain.OutputInfo{Name: "B", Width: 800, Height: 600})
		r.output <- ports.OutputHDRCaps{OutputHDRCaps: domain.OutputHDRCaps{Output: "B", Active: true, Caps: domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true, MaxLuminance: 604})}}
		r.client <- ports.WindowMapped{ID: 1, AppID: "game"}
		r.client <- ports.WindowMapped{ID: 2}
		r.settle(t)
		r.moveTo(1500, 300, 1)
		r.settle(t)
		r.client <- ports.WindowMapped{ID: 3}
		set := r.settle(t)
		one := dragRect(t, set, "A", 1)
		r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 2)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		r.moveTo(1400, 300, 3)
		set = r.settle(t)
		proxy, n := proxyIn(set, "B")
		if n != 1 || !proxy.SDRExpansion.Active() {
			t.Fatalf("proxy %+v (%d)", proxy, n)
		}
	})
}
