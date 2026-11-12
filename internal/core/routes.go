package core

import (
	"context"
	"slices"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// routeIDs includes listed, not merely visible, surfaces and capture sources.
func routeIDs(dst []WindowID, s domain.Scene) []WindowID {
	for _, w := range s.Windows {
		dst = append(dst, w.ID)
	}
	for _, l := range s.Layers {
		dst = append(dst, l.ID)
	}
	if s.CaptureScene != nil {
		dst = routeIDs(dst, *s.CaptureScene)
	}
	slices.Sort(dst)
	return slices.Compact(dst)
}

// resetRoutes forgets what was sent for the screen: it is a new output as far
// as wayland is concerned (a fresh screen, or the placeholder adopted under
// another name). Its generations start above every one core ever issued, so
// a route kept by wayland for a reused name can never outrank the new ones.
func (c *Core) resetRoutes(sc *screen) {
	sc.routes, sc.routeBuf, sc.routeSent = nil, nil, false
	sc.routeGen = c.routeHigh
}

// syncRoutes publishes only changed memberships; only gains advance the gate.
func (c *Core) syncRoutes(ctx context.Context, scenes []domain.Scene) error {
	var changed []domain.ContentRoute
	for i := range scenes {
		scene := &scenes[i]
		if scene.Output == "" {
			continue
		}
		sc := c.screens[i]
		sc.routeBuf = routeIDs(sc.routeBuf[:0], *scene)
		if !slices.Equal(sc.routes, sc.routeBuf) {
			for _, id := range sc.routeBuf {
				if _, found := slices.BinarySearch(sc.routes, id); !found {
					sc.routeGen++
					c.routeHigh = max(c.routeHigh, sc.routeGen)
					break
				}
			}
			sc.routes = append(sc.routes[:0], sc.routeBuf...)
			sc.routeSent = true
			changed = append(changed, domain.ContentRoute{Output: scene.Output, Gen: sc.routeGen, Windows: slices.Clone(sc.routes)})
		}
		// A screen that never sent a route has nothing to wait for.
		if sc.routeSent {
			scene.Routes = sc.routeGen
		}
	}
	if len(changed) > 0 {
		return c.command(ctx, ports.SetContentRoutes{Routes: changed})
	}
	return nil
}
