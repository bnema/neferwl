// Package core is the compositor's application service. Core runs as the
// owner goroutine of window state: it consumes port events, applies the
// domain model and publishes scenes and client commands. It depends only on
// the standard library, internal/ports and the domain packages below.
//
// The domain model lives in subpackages, each importing only the standard
// library, the value types of internal/ports and the packages listed before
// it (enforced by the root domainpolicy test):
//
//   - geom: placement values and geometry helpers (scales, border zones).
//   - motion: springs, swipe tracking, snap swipes and presentation slides.
//   - layout: the layout engines (scroll, fixed, cascade) over lane state.
//   - workspace: the Workspace model (columns, floats, stash, focus, actions)
//     built on layout and motion.
//
// core itself keeps orchestration: Monitor and screens, input routing,
// gestures, the overview selection, transitions, capture, security and the
// outbox. Domain code takes no context, owns no clock and never logs; core
// passes times and values in.
package core
