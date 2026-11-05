package cli

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

func addTargetFlag(fs *flag.FlagSet) *string { return fs.String("s", "", "Target session ID.") }

func resolveControl(flagS string, env func(string) string, list func() ([]sessiondir.Meta, error)) (string, error) {
	id := flagS
	if id == "" {
		id = env("NEFERWL_SESSION")
	}
	if id == "" && env("NEFERWL_CONTROL") != "" {
		return env("NEFERWL_CONTROL"), nil
	}
	sessions, err := list()
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	fail := func(reason string) (string, error) {
		return "", fmt.Errorf("%s; live sessions: %s", reason, strings.Join(ids, ", "))
	}
	if id != "" {
		if !sessiondir.ValidID(id) {
			return fail(fmt.Sprintf("invalid session id %q", id))
		}
		for _, s := range sessions {
			if s.ID == id && s.Control != "" {
				return s.Control, nil
			}
		}
		return fail(fmt.Sprintf("session %q has no control socket", id))
	}
	display := env("WAYLAND_DISPLAY")
	if display != "" {
		for _, s := range sessions {
			if s.Wayland == display && s.Control != "" {
				return s.Control, nil
			}
		}
	}
	return fail("no control target; set -s, NEFERWL_SESSION or NEFERWL_CONTROL")
}
