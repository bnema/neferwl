package domain

import "fmt"

// Backend names the output/input backend the compositor runs on.
type Backend string

const (
	BackendDRM      Backend = "drm"
	BackendHeadless Backend = "headless"
)

// ParseBackend returns the Backend named s, or an error for an unknown name.
func ParseBackend(s string) (Backend, error) {
	switch b := Backend(s); b {
	case BackendDRM, BackendHeadless:
		return b, nil
	}
	return "", fmt.Errorf("unknown backend %q (want drm or headless)", s)
}
