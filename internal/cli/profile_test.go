package cli

import (
	"flag"
	"io"
	"testing"

	"github.com/bnema/neferwl/internal/core"
)

func TestProfileFlags(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		bad     bool
		profile string
		heads   int
		input   core.InputSource
	}{
		{[]string{"--backend=headless"}, false, "ephemeral", 1, core.InputNone},
		{[]string{"--backend=headless", "--profile=persistent"}, false, "persistent", 0, core.InputLibinputDirect},
		{[]string{"--backend=server", "--size=100x80", "--timeout=1s"}, false, "persistent", 1, core.InputLibinputDirect},
		{[]string{"--backend=headless", "--profile=persistent", "--input-devices=none"}, false, "persistent", 0, core.InputNone},
		{[]string{"--backend=headless", "--input-devices=grab"}, false, "ephemeral", 1, core.InputLibinputDirect},
		{[]string{"--profile=ephemeral"}, true, "", 0, 0},
		{[]string{"--backend=headless", "--profile=bad"}, true, "", 0, 0},
		{[]string{"--input-devices=none"}, true, "", 0, 0},
		{[]string{"--backend=headless", "--input-devices=bad"}, true, "", 0, 0},
	} {
		var f runFlags
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f.register(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
		sizes, err := f.validate(fs.Args(), set)
		if (err != nil) != tc.bad {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if tc.bad {
			continue
		}
		plan := core.PlanBackend("headless", mustProfile(f.profile), f.overrides)
		if f.profile != tc.profile || len(sizes) != tc.heads || plan.Input != tc.input {
			t.Fatalf("%v: profile %s sizes %v plan %+v", tc.args, f.profile, sizes, plan)
		}
	}
}
