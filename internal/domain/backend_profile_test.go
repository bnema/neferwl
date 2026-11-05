package domain

import "testing"

func TestProfile(t *testing.T) {
	for _, p := range []Profile{ProfileEphemeral, ProfilePersistent} {
		got, err := ParseProfile(p.String())
		if err != nil || got != p {
			t.Fatal(got, err)
		}
	}
	for _, s := range []string{"", "Persistent", "server"} {
		if _, err := ParseProfile(s); err == nil {
			t.Fatal(s)
		}
	}
	if Profile(99).String() != "Profile(99)" {
		t.Fatal(Profile(99).String())
	}
}
