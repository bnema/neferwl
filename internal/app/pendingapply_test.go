package app

import (
	"errors"
	"testing"
)

func TestApplyProgress(t *testing.T) {
	boom := errors.New("modeset")
	for _, tc := range []struct {
		name    string
		run     func(*applyProgress, <-chan error, <-chan error) applyDecision
		failure bool
	}{
		{"success", func(p *applyProgress, a, _ <-chan error) applyDecision {
			p.start(1, map[string]bool{"A": true}, nil)
			p.scanned(map[string]<-chan error{"A": a})
			return p.readyEvent("A", a, nil)
		}, false},
		{"ready error rollback", func(p *applyProgress, a, _ <-chan error) applyDecision {
			p.start(1, map[string]bool{"A": true}, nil)
			p.scanned(map[string]<-chan error{"A": a})
			return p.readyEvent("A", a, boom)
		}, true},
		{"timeout", func(p *applyProgress, a, _ <-chan error) applyDecision {
			p.start(1, map[string]bool{"A": true}, nil)
			p.scanned(map[string]<-chan error{"A": a})
			return p.timeout(1)
		}, true},
		{"restart replacement missing", func(p *applyProgress, a, _ <-chan error) applyDecision {
			p.start(1, map[string]bool{"A": true}, map[string]bool{"A": true})
			p.stopped("A", a, true, nil)
			return p.scanned(nil)
		}, true},
		{"unchanged not ready", func(p *applyProgress, a, _ <-chan error) applyDecision {
			p.start(1, map[string]bool{"A": true}, nil)
			return p.scanned(map[string]<-chan error{"A": a})
		}, false},
		{"hotplug during apply", func(p *applyProgress, a, b <-chan error) applyDecision {
			p.start(1, map[string]bool{"A": true, "B": true}, nil)
			p.scanned(map[string]<-chan error{"A": a, "B": b})
			p.readyEvent("A", a, nil)
			return p.readyEvent("B", b, nil)
		}, false},
		{"stale timeout", func(p *applyProgress, a, _ <-chan error) applyDecision {
			p.start(1, map[string]bool{"A": true}, nil)
			p.start(2, map[string]bool{"A": true}, nil)
			p.scanned(map[string]<-chan error{"A": a})
			p.timeout(1)
			return p.readyEvent("A", a, nil)
		}, false},
		{"disabled head stop", func(p *applyProgress, a, _ <-chan error) applyDecision {
			p.start(1, nil, map[string]bool{"A": false})
			return p.stopped("A", a, false, nil)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &applyProgress{}
			a, b := make(chan error), make(chan error)
			d := tc.run(p, a, b)
			if tc.name == "unchanged not ready" {
				if d.reply || !p.active {
					t.Fatalf("premature: %+v", d)
				}
				return
			}
			if !d.reply || (d.err != nil) != tc.failure || d.rollback != tc.failure {
				t.Fatalf("decision: %+v", d)
			}
		})
	}
}
