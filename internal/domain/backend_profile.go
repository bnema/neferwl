package domain

import "fmt"

// Profile selects the lifetime and device defaults of a headless session.
// The zero value is the ephemeral, isolated test/automation profile.
type Profile uint8

const (
	ProfileEphemeral Profile = iota
	ProfilePersistent
)

func (p Profile) String() string {
	switch p {
	case ProfileEphemeral:
		return "ephemeral"
	case ProfilePersistent:
		return "persistent"
	default:
		return fmt.Sprintf("Profile(%d)", p)
	}
}

func ParseProfile(s string) (Profile, error) {
	switch s {
	case "ephemeral":
		return ProfileEphemeral, nil
	case "persistent":
		return ProfilePersistent, nil
	default:
		return 0, fmt.Errorf("unknown profile %q (want ephemeral or persistent)", s)
	}
}
