package core

import (
	"errors"
	"reflect"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestPlanCards(t *testing.T) {
	c := []domain.GPUInfo{{Path: "card0"}, {Path: "card1"}, {Path: "card2"}}
	for _, tc := range []struct {
		name string
		sel  domain.GPUSelection
		want []CardPlan
	}{
		{"ignored absent", domain.GPUSelection{Keep: []int{0, 2}, Render: 2}, []CardPlan{{c[0], false}, {c[2], true}}},
		{"selected only and order", domain.GPUSelection{Keep: []int{0, 1, 2}, Render: 1}, []CardPlan{{c[0], false}, {c[1], true}, {c[2], false}}},
		{"empty", domain.GPUSelection{}, []CardPlan{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanCards(c, tc.sel); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%+v want %+v", got, tc.want)
			}
		})
	}
}

func TestRenderOpenOrder(t *testing.T) {
	c := []domain.GPUInfo{{Path: "card0", Render: 1}, {Path: "card1"}, {Path: "card2", Render: 2}, {Path: "card3", Render: 3}}
	plans := []CardPlan{{Info: c[0]}, {Info: c[1]}, {Info: c[2], Render: true}, {Info: c[3]}}
	for _, strict := range []bool{false, true} {
		want := []domain.GPUInfo{c[2]}
		if !strict {
			want = append(want, c[0], c[3])
		}
		if got := RenderOpenOrder(plans, strict); !reflect.DeepEqual(got, want) {
			t.Fatalf("strict=%v: %v want %v", strict, got, want)
		}
	}
	if got := RenderOpenOrder(nil, false); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestPlanHeadlessRender(t *testing.T) {
	boom := errors.New("discovery failed")
	gpu := domain.GPUInfo{Path: "/dev/dri/card0", Render: 128, PCI: "0000:03:00.0"}
	for _, tc := range []struct {
		name                 string
		cands                []domain.GPUInfo
		listErr              error
		cfg                  GPUSelectConfig
		constrained, matched bool
		choice               HeadlessRenderChoice
		wantErr              bool
	}{
		{name: "discovery fallback", listErr: boom, choice: HeadlessFallbackFirstDevice, wantErr: true},
		{name: "empty fallback", choice: HeadlessFallbackFirstDevice, wantErr: true},
		{name: "no renderer fallback", cands: []domain.GPUInfo{{Path: "card0"}}, choice: HeadlessFallbackFirstDevice, wantErr: true},
		{name: "constrained discovery", listErr: boom, constrained: true, choice: HeadlessFail, wantErr: true},
		{name: "constrained empty", constrained: true, choice: HeadlessFail, wantErr: true},
		{name: "unknown identifier", cands: []domain.GPUInfo{gpu}, cfg: GPUSelectConfig{Render: "unknown"}, constrained: true, choice: HeadlessFail, wantErr: true},
		{name: "selection", cands: []domain.GPUInfo{gpu}, choice: HeadlessUseSelection},
		{name: "device constrained selection", cands: []domain.GPUInfo{gpu}, cfg: GPUSelectConfig{Devices: []string{gpu.PCI}}, constrained: true, choice: HeadlessUseSelection},
		{name: "ignore constrained selection", cands: []domain.GPUInfo{gpu, {Path: "/dev/dri/card1"}}, cfg: GPUSelectConfig{Ignore: []string{"/dev/dri/card1"}}, constrained: true, choice: HeadlessUseSelection},
		{name: "strict selection", cands: []domain.GPUInfo{gpu}, cfg: GPUSelectConfig{Render: gpu.PCI}, constrained: true, choice: HeadlessUseSelection},
		{name: "explicit without discovery", listErr: boom, cfg: GPUSelectConfig{Render: "/dev/dri/renderD128"}, constrained: true, choice: HeadlessUseExplicitRenderNode},
		{name: "explicit matched", cands: []domain.GPUInfo{gpu}, cfg: GPUSelectConfig{Render: "/dev/dri/renderD128"}, constrained: true, matched: true, choice: HeadlessUseSelection},
		{name: "explicit with ignore", cfg: GPUSelectConfig{Render: "/dev/dri/renderD128", Ignore: []string{"unknown"}}, constrained: true, choice: HeadlessFail, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanHeadlessRender(tc.cands, tc.listErr, tc.cfg, tc.constrained, tc.matched)
			if got.Choice != tc.choice || (got.Err != nil) != tc.wantErr {
				t.Fatalf("%+v", got)
			}
			if tc.listErr != nil && tc.choice != HeadlessUseExplicitRenderNode && !errors.Is(got.Err, tc.listErr) {
				t.Fatal(got.Err)
			}
			if got.FallbackAfterFailure != (tc.choice == HeadlessUseSelection && !tc.constrained) {
				t.Fatalf("fallback after failure: %+v", got)
			}
			if got.Choice == HeadlessUseSelection {
				if len(got.Plans) != 1 || got.Plans[0].Info != gpu || !got.Plans[0].Render || got.Strict != (tc.cfg.Render != "") || len(got.Reasons) == 0 {
					t.Fatalf("%+v", got)
				}
			}
		})
	}
}
