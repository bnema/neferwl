package core

import (
	"errors"
	"reflect"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestLeaseGuard(t *testing.T) {
	for _, tt := range []struct {
		name          string
		before, after domain.SecurityState
		want          string
	}{
		{name: "initial unprotected"},
		{name: "stable generation", before: domain.SecurityState{Generation: 4}, after: domain.SecurityState{Generation: 4}},
		{name: "protected before", before: domain.SecurityState{Protected: true}, want: "session protected"},
		{name: "protected throughout", before: domain.SecurityState{Generation: 4, Protected: true}, after: domain.SecurityState{Generation: 4, Protected: true}, want: "session protected"},
		{name: "protected during acquisition", before: domain.SecurityState{Generation: 4}, after: domain.SecurityState{Generation: 4, Protected: true}, want: "session transition during lease"},
		{name: "complete protection cycle", before: domain.SecurityState{Generation: 4}, after: domain.SecurityState{Generation: 5}, want: "session transition during lease"},
		{name: "generation regression", before: domain.SecurityState{Generation: 4}, after: domain.SecurityState{Generation: 3}, want: "session transition during lease"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := LeaseGuard(tt.before, tt.after)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tt.want {
				t.Fatalf("LeaseGuard() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestOutputPolicyHotplugRecovery(t *testing.T) {
	p := OutputPolicy{RenderCard: "card0", Recovering: true}
	add := domain.HotplugEvent{Card: "card1", Action: domain.HotplugCardAdd}
	if got := p.OnHotplug(add); len(got) != 0 {
		t.Fatalf("opened during recovery: %v", got)
	}
	p.OnHotplug(add)
	if got := p.PendingCards(); !reflect.DeepEqual(got, []domain.CardName{"card1"}) {
		t.Fatal(got)
	}
	p.OnHotplug(add)
	p.OnHotplug(domain.HotplugEvent{Card: "card1", Action: domain.HotplugCardRemove})
	if got := p.PendingCards(); len(got) != 0 {
		t.Fatal(got)
	}
	got := p.OnHotplug(domain.HotplugEvent{Card: "card0", Action: domain.HotplugCardRemove})
	if len(got) != 1 || !errors.Is(got[0].(ActExit).Err, ErrRenderGPURemoved) {
		t.Fatal(got)
	}
	p.Recovering = false
	if got := p.OnHotplug(add); !reflect.DeepEqual(got, []OutputAction{ActOpenCard{Card: "card1"}, ActScan{}}) {
		t.Fatal(got)
	}
}

func TestOutputPolicyReplug(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		p := OutputPolicy{Recovering: recovering}
		p.OnCardOpen("card1", HotplugAddDying)
		p.OnCardOpen("card1", HotplugAddDying)
		got := p.OnCardClosed("card1")
		if recovering {
			if len(got) != 0 || !reflect.DeepEqual(p.PendingCards(), []domain.CardName{"card1"}) {
				t.Fatal(got)
			}
		} else if !reflect.DeepEqual(got, []OutputAction{ActOpenCard{Card: "card1"}}) {
			t.Fatal(got)
		}
		if got := p.OnCardClosed("card1"); len(got) != 0 {
			t.Fatal(got)
		}
		p.OnCardOpen("card1", HotplugAddDying)
		p.OnHotplug(domain.HotplugEvent{Card: "card1", Action: domain.HotplugCardRemove})
		if got := p.OnCardClosed("card1"); len(got) != 0 {
			t.Fatal(got)
		}
		p.OnCardOpen("card1", HotplugAddFailed)
		if got := p.OnCardClosed("card1"); len(got) != 0 {
			t.Fatal(got)
		}
	}
}

func TestOutputPolicyReaderFailure(t *testing.T) {
	err := errors.New("reader failed")
	for _, render := range []bool{false, true} {
		for _, removed := range []bool{false, true} {
			for _, exists := range []bool{false, true} {
				for _, noDevice := range []bool{false, true} {
					p := OutputPolicy{RenderCard: "card0"}
					card := domain.CardName("card1")
					if render {
						card = "card0"
					}
					got := p.OnReaderFailure(card, removed, exists, noDevice, err)
					if removed {
						if len(got) != 0 {
							t.Fatal(got)
						}
						continue
					}
					if len(got) != 1 {
						t.Fatal(got)
					}
					if !render && noDevice {
						if got[0] != (ActRemoveCard{Card: card}) {
							t.Fatal(got)
						}
						continue
					}
					exit := got[0].(ActExit)
					if !errors.Is(exit.Err, err) || errors.Is(exit.Err, ErrRenderGPURemoved) != (render && (!exists || noDevice)) {
						t.Fatal(got)
					}
				}
			}
		}
	}
}

func TestOutputPolicyLeaseDecision(t *testing.T) {
	var p OutputPolicy
	for _, active := range []bool{false, true} {
		for _, forwarded := range []bool{false, true} {
			for _, gate := range []bool{false, true} {
				got := p.OnLeaseRequest(active, domain.SecurityState{Protected: forwarded}, domain.SecurityState{Protected: gate})
				if (got.Err == nil) != (active && !forwarded && !gate) {
					t.Fatalf("active=%v forwarded=%v gate=%v: %v", active, forwarded, gate, got.Err)
				}
			}
		}
	}
}

func TestOutputPolicyBarrierBudget(t *testing.T) {
	var p OutputPolicy
	err := errors.New("revoke failed")
	if !p.OnBarrier(err) {
		t.Fatal("first retry denied")
	}
	p.OnSecurity(false)
	if !p.OnBarrier(err) {
		t.Fatal("second retry denied")
	}
	if p.OnBarrier(err) {
		t.Fatal("third retry allowed")
	}
	p.OnSecurity(true)
	if !p.OnBarrier(err) {
		t.Fatal("new generation did not reset budget")
	}
	if p.OnBarrier(nil) {
		t.Fatal("successful barrier retried")
	}
	if p.OnBarrier(err) {
		t.Fatal("successful attempt not counted")
	}
}

func TestOutputPolicyFailureAndRetirement(t *testing.T) {
	var p OutputPolicy
	if p.OnOutputFailure(errors.New("renderer failed")) {
		t.Fatal("unrelated failure starts recovery")
	}
	if !p.OnOutputFailure(ports.ErrDeviceLost) || p.OnOutputFailure(ports.ErrDeviceLost) {
		t.Fatal("recovery must start once")
	}
	p.OnHotplug(domain.HotplugEvent{Card: "card2", Action: domain.HotplugCardAdd})
	if got := p.OnReopened(); !reflect.DeepEqual(got, []domain.CardName{"card2"}) || p.Recovering {
		t.Fatal(got)
	}
	for _, inactive := range []bool{false, true} {
		for _, removed := range []bool{false, true} {
			for _, exists := range []bool{false, true} {
				got := p.OnOutputStopped(42, inactive, removed, exists)
				if inactive || (removed && !exists) {
					if !reflect.DeepEqual(got, []OutputAction{ActRetireInstance{Instance: 42}}) {
						t.Fatal(got)
					}
				} else if len(got) != 0 {
					t.Fatal(got)
				}
			}
		}
	}
	if !p.RestartOutput(true, nil) || p.RestartOutput(false, nil) || p.RestartOutput(true, errors.New("failure")) {
		t.Fatal("restart decision")
	}
}

func TestDiscardedOutputPolicy(t *testing.T) {
	instance, err := RegisterDiscardedOutput(41)
	if instance != 42 || err != nil {
		t.Fatalf("%v %v", instance, err)
	}
	instance, err = RegisterDiscardedOutput(^domain.OutputInstance(0))
	if instance != 0 || err == nil || err.Error() != "output instance exhausted" {
		t.Fatalf("%v %v", instance, err)
	}
	if err := RetireDiscardedOutput(42, true); err != nil {
		t.Fatal(err)
	}
	if err := RetireDiscardedOutput(42, false); err == nil || err.Error() != "discarded output 42 shutdown inactivity unconfirmed" {
		t.Fatal(err)
	}
}
