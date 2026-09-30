// Package workspaceid names workspaces for programs outside the compositor:
// the `id` of ext_workspace_handle_v1 and the `workspace_id` of the state
// file are the same string.
//
// Core keeps numeric workspace IDs, unique for the life of the process. A
// workspace declared in the configuration is named after its configured name
// ("name:<configured name>"), so its ID survives restarts; any other
// workspace is "<prefix>-<core ID>", where the prefix is drawn once per
// compositor launch, so such an ID is unique within a launch and never
// repeats across launches.
package workspaceid

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
)

// IDs builds workspace ID strings for one compositor launch.
type IDs struct{ prefix string }

// New draws the launch prefix: 8 lowercase hex characters from crypto/rand.
func New() *IDs {
	var b [4]byte
	// rand.Read never returns an error (Go 1.24+): it stops the program
	// instead of returning weak randomness.
	_, _ = rand.Read(b[:])
	return &IDs{prefix: hex.EncodeToString(b[:])}
}

// WithPrefix uses a fixed launch prefix (tests).
func WithPrefix(prefix string) *IDs { return &IDs{prefix: prefix} }

// ID is the string of the workspace with this core ID and configured name
// (empty for a numbered or dynamic workspace).
func (i *IDs) ID(id uint64, configured string) string {
	if configured != "" {
		return "name:" + configured
	}
	return i.prefix + "-" + strconv.FormatUint(id, 10)
}
