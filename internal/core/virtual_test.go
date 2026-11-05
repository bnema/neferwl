package core

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestDesiredVirtuals(t *testing.T) {
	tv := domain.OutputConfig{Name: "tv", Virtual: true, Mode: "1920x1080"}
	file := domain.Config{Outputs: []domain.OutputConfig{{Name: "DP-1"}, tv, {Name: "tablet", Virtual: true, Mode: "1x1"}}}
	state := NewOutputOverrides(file, false)
	got := state.Virtuals()
	if len(got) != 2 || !sameVirtualEntry(got[0], tv) || got[1].Name != "tablet" {
		t.Fatalf("virtuals=%+v", got)
	}
	got[0].Mode = "changed"
	if state.Virtuals()[0].Mode != tv.Mode {
		t.Fatal("returned slice aliases file")
	}
	state.SetHeads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "tv"}}, {Info: domain.OutputInfo{Name: "tablet", Virtual: true}}}})
	if got := state.Virtuals(); len(got) != 1 || got[0].Name != "tablet" {
		t.Fatalf("collision: %+v", got)
	}
	state.Reload(domain.Config{})
	if len(state.Virtuals()) != 0 {
		t.Fatal("removed virtual survived")
	}
	state.Reload(domain.Config{Outputs: []domain.OutputConfig{tv}})
	state.SetHeads(domain.OutputHeads{})
	if len(state.Virtuals()) != 1 {
		t.Fatal("add failed")
	}
}

func TestVirtualModeReload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domain.Config)
		want   string
	}{
		{"unchanged", func(*domain.Config) {}, "1280x720@60.000"},
		{"unrelated", func(c *domain.Config) { c.Layout.Gaps = 4 }, "1280x720@60.000"},
		{"mode", func(c *domain.Config) { c.Outputs[0].Mode = "2560x1440@120" }, "2560x1440@120"},
		{"keep-focus", func(c *domain.Config) { c.Outputs[0].KeepFocus = true }, "1280x720@60.000"},
		{"other entry field", func(c *domain.Config) { c.Outputs[0].Primary = true }, "1920x1080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "1920x1080"}}}
			state := NewOutputOverrides(file, false)
			mode := domain.OutputMode{Width: 1280, Height: 720, RefreshMilli: 60000}
			state.SetHeads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "tv", Virtual: true}, Modes: []domain.OutputMode{mode}}}})
			if _, err := state.Apply(domain.OutputApply{Heads: []domain.HeadChange{{Name: "tv", Enabled: true, Mode: &mode}}}); err != nil {
				t.Fatal(err)
			}
			// Overrides must also apply while inventory is temporarily absent.
			state.SetHeads(domain.OutputHeads{})
			if state.Virtuals()[0].Mode != "1280x720@60.000" {
				t.Fatal(state.Virtuals())
			}
			next := domain.Config{Outputs: append([]domain.OutputConfig(nil), file.Outputs...)}
			tc.change(&next)
			state.Reload(next)
			want := tc.want
			if got := state.Virtuals(); len(got) != 1 || got[0].Mode != want {
				t.Fatalf("got=%+v want=%s", got, want)
			}
			state.Reload(domain.Config{})
			state.Reload(file)
			if state.Virtuals()[0].Mode != file.Outputs[0].Mode {
				t.Fatal("removed override resurrected")
			}
		})
	}
}

func TestVirtualCollisionWarnings(t *testing.T) {
	file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "10x10"}}}
	a := NewOutputApply(NewOutputOverrides(file, false))
	physical := domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "tv"}}}}
	a.Heads(physical)
	if len(a.Virtuals()) != 0 {
		t.Fatal("collision retained")
	}
	warnings := a.TakeWarnings()
	if len(warnings) != 1 || warnings[0].Name != "tv" || warnings[0].Message == "" {
		t.Fatal(warnings)
	}
	if ch, _ := a.ReplyOut(make(chan domain.OutputApplied)); ch != nil {
		t.Fatal("fabricated reply")
	}
	a.Heads(physical)
	a.Reload(file)
	if len(a.TakeWarnings()) != 0 {
		t.Fatal("duplicate warning")
	}
	a.Heads(domain.OutputHeads{})
	a.Heads(physical)
	if len(a.TakeWarnings()) != 1 {
		t.Fatal("new collision not warned")
	}
	a.Reload(domain.Config{})
	a.Reload(file)
	if len(a.TakeWarnings()) != 1 {
		t.Fatal("reload collision not warned")
	}
	a.Heads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "tv", Virtual: true}}}})
	if len(a.Virtuals()) != 1 || len(a.TakeWarnings()) != 0 {
		t.Fatal("virtual head collides with itself")
	}
}
