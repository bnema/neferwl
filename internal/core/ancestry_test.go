package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestProcessAncestryPlacement(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		chain                                       []int
		rule, floating, shared, remap, auto, reused bool
		want                                        string
	}{
		{name: "descendant", chain: []int{55, 4134801}, want: "tv"},
		{name: "auto fullscreen", chain: []int{4134801}, auto: true, want: "tv"},
		{name: "nearest", chain: []int{77, 4134801}, want: "DP-2"},
		{name: "reused PID", chain: []int{4134801}, reused: true, want: "DP-2"},
		{name: "no match", chain: []int{55}, want: "DP-2"},
		{name: "explicit rule", chain: []int{4134801}, rule: true, want: "DP-2"},
		{name: "floating", chain: []int{4134801}, floating: true, want: "tv"},
		{name: "bridge", chain: []int{4134801}, shared: true, want: "DP-2"},
		{name: "remap", chain: []int{4134801}, remap: true, want: "DP-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tv := left
				tv.Name = "tv"
				r := startMulti(t, func(c *domain.Config) {
					c.Rules = []domain.WindowRule{winRule("parent", "steam", func(r *domain.WindowRule) { r.Output = "tv" })}
					if tc.rule {
						c.Rules = append(c.Rules, winRule("child", "steam_app_892970", func(r *domain.WindowRule) { r.Output = "DP-2" }))
					}
				}, tv, right)
				r.client <- ports.WindowMapped{ID: 1, AppID: "steam", PID: 4134801, Proc: procNodes(4134801), Fullscreen: true, Xwayland: true}
				parent := stWindow(r.stateWith(t, 1), 1)
				r.key(t, "Right", domain.ModAlt|domain.ModCtrl)
				r.client <- ports.WindowMapped{ID: 2, AppID: "other", PID: 77, Proc: procNodes(77)}
				if st := r.stateWith(t, 2); st.Output != "DP-2" {
					t.Fatalf("setup focus: %+v", st)
				}
				proc := procNodes(append([]int{4139428}, tc.chain...)...)
				if tc.reused {
					proc[1].Key.Start++
				}
				r.client <- ports.WindowMapped{ID: 3, AppID: "steam_app_892970", PID: 4139428, Proc: proc, PIDShared: tc.shared, Remap: tc.remap, Floating: tc.floating, Width: 30, Height: 20, Fullscreen: !tc.floating && !tc.auto, HasAt: true, AtX: 200}
				if tc.auto {
					r.client <- ports.WindowResized{ID: 3, Width: tv.Width, Height: tv.Height}
				}
				st := r.stateWith(t, 3)
				child := stWindow(st, 3)
				if child.Output != tc.want {
					t.Fatalf("destination: %+v", child)
				}
				if tc.want == "tv" && child.WorkspaceID != parent.WorkspaceID {
					t.Fatalf("workspace: parent %+v child %+v", parent, child)
				}
				if tc.want == "tv" && (st.Output != "tv" || st.Window == nil || st.Window.ID != 3) {
					t.Fatalf("focus did not follow: %+v", st)
				}
				if !tc.floating && !tc.remap && !child.Fullscreen {
					t.Fatalf("fullscreen lost: %+v", child)
				}
				if !tc.floating && !tc.remap {
					synctest.Wait()
					var configured ports.ConfigureWindow
				drain:
					for {
						select {
						case cmd := <-r.commands:
							if v, ok := cmd.(ports.ConfigureWindow); ok && v.ID == 3 {
								configured = v
							}
						default:
							break drain
						}
					}
					width, height := right.Width, right.Height
					if tc.want == "tv" {
						width, height = tv.Width, tv.Height
					}
					if !configured.Fullscreen || configured.Width != width || configured.Height != height {
						t.Fatalf("destination fullscreen configure: %+v, want %dx%d", configured, width, height)
					}
				}
			})
		})
	}
}

func procNodes(pids ...int) []domain.ProcNode {
	nodes := make([]domain.ProcNode, len(pids))
	for i, pid := range pids {
		nodes[i] = domain.ProcNode{Key: domain.ProcKey{PID: pid, Start: 10}}
	}
	return nodes
}
