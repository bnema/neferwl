// Package layout owns column geometry, lane state and layout engine hooks.
// All mutable state belongs to a single owner goroutine. It depends only on
// domain geometry and values, never workspace presentation or plumbing.
package layout
