// Package layout owns column geometry, lane state and layout engine hooks.
// All mutable state belongs to a single owner goroutine. It depends only on
// stdlib, internal/domain and internal/core/geom, never ports or adapters.
package layout
