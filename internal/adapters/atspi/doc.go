// Package atspi finds hint targets in the accessibility tree of the apps on
// screen. It talks to the AT-SPI registry over the accessibility bus with
// zerobus, from one connection used by one goroutine at a time, and pipelines
// the calls it needs: several are sent before their replies are read, and the
// replies are matched by serial.
//
// The approach (Collection.GetMatches on the window frame, bounded walk when
// the toolkit has no Collection, window-relative extents) is adapted from
// neru's AT-SPI client, https://github.com/y3owk1n/neru (MIT licence).
package atspi
