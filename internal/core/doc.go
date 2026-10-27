// Package core is the compositor's application service. Core runs as the
// owner goroutine of window state: it consumes port events, applies the
// domain model and publishes scenes and client commands. It depends only on
// the standard library, internal/domain, internal/ports and its subpackages.
//
// Entities and value types live in internal/domain, which imports only the
// standard library. Ports defines interfaces and sealed messages carrying those
// values. Core applies the model through these subpackages:
//
//   - geom: placement values and geometry helpers (scales, border zones).
//   - motion: springs, swipe tracking, snap swipes and presentation slides.
//   - layout: the layout engines (scroll, fixed, cascade) over lane state.
//   - workspace: the Workspace model (columns, floats, stash, focus, actions)
//     built on layout and motion.
//
// Clock and token effects use ports.Clock and ports.TokenSource; core never
// calls the wall clock or imports crypto/rand or reflect. GPU, hotplug and
// output-apply policies return decisions for adapters to execute synchronously.
// Security/recovery ordering remains in drm/supervisor by design.
//
// core itself keeps orchestration: Monitor and screens, input routing,
// gestures, the overview selection, transitions, capture, security and the
// outbox. Domain code takes no context, owns no clock and never logs; core
// passes times and values in.
package core
