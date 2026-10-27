package core

import (
	"errors"
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestApplyProgress(t *testing.T) {
	boom := errors.New("modeset")
	for _, tc := range []struct {
		name    string
		run     func(*ApplyProgress, domain.OutputInstance, domain.OutputInstance) ApplyDecision
		failure bool
	}{
		{"success", func(p *ApplyProgress, a, _ domain.OutputInstance) ApplyDecision {
			p.Start(1, map[string]bool{"A": true}, nil)
			p.Scanned(map[string]domain.OutputInstance{"A": a})
			return p.ReadyEvent("A", a, a, nil)
		}, false},
		{"scan error with previously Ready output", func(p *ApplyProgress, a, _ domain.OutputInstance) ApplyDecision {
			p.completed = map[domain.OutputInstance]bool{a: true}
			p.Start(1, map[string]bool{"A": true}, nil)
			d := p.ScanError(boom)
			if later := p.Scanned(map[string]domain.OutputInstance{"A": a}); later.Reply {
				t.Fatalf("old Ready output completed failed apply: %+v", later)
			}
			return d
		}, true},
		{"Ready error rollback", func(p *ApplyProgress, a, _ domain.OutputInstance) ApplyDecision {
			p.Start(1, map[string]bool{"A": true}, nil)
			p.Scanned(map[string]domain.OutputInstance{"A": a})
			return p.ReadyEvent("A", a, a, boom)
		}, true},
		{"timeout", func(p *ApplyProgress, a, _ domain.OutputInstance) ApplyDecision {
			p.Start(1, map[string]bool{"A": true}, nil)
			p.Scanned(map[string]domain.OutputInstance{"A": a})
			return p.Timeout(1)
		}, true},
		{"restart replacement missing", func(p *ApplyProgress, a, _ domain.OutputInstance) ApplyDecision {
			p.Start(1, map[string]bool{"A": true}, map[string]bool{"A": true})
			p.Stopped("A", a, true, nil)
			return p.Scanned(nil)
		}, true},
		{"unchanged not Ready", func(p *ApplyProgress, a, _ domain.OutputInstance) ApplyDecision {
			p.Start(1, map[string]bool{"A": true}, nil)
			return p.Scanned(map[string]domain.OutputInstance{"A": a})
		}, false},
		{"hotplug during apply", func(p *ApplyProgress, a, b domain.OutputInstance) ApplyDecision {
			p.Start(1, map[string]bool{"A": true, "B": true}, nil)
			p.Scanned(map[string]domain.OutputInstance{"A": a, "B": b})
			p.ReadyEvent("A", a, a, nil)
			return p.ReadyEvent("B", b, b, nil)
		}, false},
		{"stale timeout", func(p *ApplyProgress, a, _ domain.OutputInstance) ApplyDecision {
			p.Start(1, map[string]bool{"A": true}, nil)
			p.Start(2, map[string]bool{"A": true}, nil)
			p.Scanned(map[string]domain.OutputInstance{"A": a})
			p.Timeout(1)
			return p.ReadyEvent("A", a, a, nil)
		}, false},
		{"stopped instance error ignored", func(p *ApplyProgress, a, b domain.OutputInstance) ApplyDecision {
			p.Start(1, map[string]bool{"A": true}, map[string]bool{"A": true})
			p.Stopped("A", a, true, nil)
			p.Scanned(map[string]domain.OutputInstance{"A": b})
			if d := p.ReadyEvent("A", a, b, boom); d.Reply {
				t.Fatalf("stopped instance failed the apply: %+v", d)
			}
			return p.ReadyEvent("A", b, b, nil)
		}, false},
		{"disabled head stop", func(p *ApplyProgress, a, _ domain.OutputInstance) ApplyDecision {
			p.Start(1, nil, map[string]bool{"A": false})
			return p.Stopped("A", a, false, nil)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &ApplyProgress{}
			a, b := domain.OutputInstance(1), domain.OutputInstance(2)
			d := tc.run(p, a, b)
			if tc.name == "unchanged not Ready" {
				if d.Reply || !p.Active {
					t.Fatalf("premature: %+v", d)
				}
				return
			}
			if !d.Reply || (d.Err != nil) != tc.failure {
				t.Fatalf("decision: %+v", d)
			}
			if tc.name == "scan error with previously Ready output" && !errors.Is(d.Err, boom) {
				t.Fatalf("scan error lost: %v", d.Err)
			}
		})
	}
}
