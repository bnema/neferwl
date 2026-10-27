// Package workspace is the Workspace model: columns, floats, the stash, focus
// and MRU order, fullscreen and maximization, drag and drop, overview stacks
// and the pure part of action handling. It builds on layout for lane state
// and on motion for presentation slides. Imports are limited to stdlib,
// internal/domain and core/{geom,layout,motion}, never ports or adapters.
//
// A Workspace is owned by the core goroutine and has no locks. Callers pass
// times, sizes and window facts in as values; the package takes no context,
// owns no clock and never logs.
package workspace
