package core

import (
	"errors"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestHeadlessVirtualReloadTransactions(t *testing.T) {
	file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "1920x1080"}}}
	a := NewOutputApply(NewOutputOverrides(domain.Config{}, true))
	a.Reload(file)
	if cfg, ok := a.Next(); !ok || len(cfg.Outputs) != 1 {
		t.Fatalf("add: %+v %v", cfg, ok)
	}
	a.Finished(nil)
	file.Outputs[0].Mode = "1280x720@30"
	a.Reload(file)
	if cfg, ok := a.Next(); !ok || cfg.Outputs[0].Mode != "1280x720@30" {
		t.Fatalf("mode: %+v %v", cfg, ok)
	}
	a.Finished(nil)
	a.Reload(domain.Config{})
	if cfg, ok := a.Next(); !ok || len(cfg.Outputs) != 0 {
		t.Fatalf("remove: %+v %v", cfg, ok)
	}
	a.Finished(nil)
	if _, ok := a.Next(); ok {
		t.Fatal("extra transaction")
	}
}

func TestHeadlessVirtualRequestRollback(t *testing.T) {
	file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "1920x1080"}}}
	state := NewOutputOverrides(file, true)
	mode := domain.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	state.SetHeads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "tv", Virtual: true}, Enabled: true, Current: &mode, Modes: []domain.OutputMode{mode}}}})
	a := NewOutputApply(state)
	req := domain.OutputApply{ID: 1, Heads: []domain.HeadChange{{Name: "tv", Enabled: true, Custom: true, Mode: &domain.OutputMode{Width: 1280, Height: 720}}}}
	a.Request(req)
	replies := make(chan domain.OutputApplied)
	if ch, _ := a.ReplyOut(replies); ch != nil {
		t.Fatal("replied before backend")
	}
	if cfg, ok := a.Next(); !ok || cfg.Outputs[0].Mode != "1280x720@60.000" {
		t.Fatalf("apply: %+v %v", cfg, ok)
	}
	failure := errors.New("renderer failed")
	a.Finished(failure)
	if cfg, ok := a.Next(); !ok || cfg.Outputs[0].Mode != "1920x1080" {
		t.Fatalf("rollback: %+v %v", cfg, ok)
	}
	a.Finished(nil)
	if ch, reply := a.ReplyOut(replies); ch == nil || reply.ID != 1 || !errors.Is(reply.Err, failure) {
		t.Fatalf("reply: %+v", reply)
	}
	a.ReplySent()
	a.Request(req)
	if _, ok := a.Next(); !ok {
		t.Fatal("retry not queued")
	}
	a.Finished(nil)
	if ch, reply := a.ReplyOut(replies); ch == nil || reply.Err != nil {
		t.Fatalf("success reply: %+v", reply)
	}
}

func TestHeadlessVirtualTestRequestIsImmediate(t *testing.T) {
	state := NewOutputOverrides(domain.Config{}, true)
	state.SetHeads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "tv", Virtual: true}, Enabled: true}}})
	a := NewOutputApply(state)
	a.Request(domain.OutputApply{ID: 1, Test: true, Heads: []domain.HeadChange{{Name: "tv", Enabled: true, Custom: true, Mode: &domain.OutputMode{Width: 10, Height: 10}}}})
	if _, ok := a.Next(); ok {
		t.Fatal("test queued backend work")
	}
	if ch, reply := a.ReplyOut(make(chan domain.OutputApplied)); ch == nil || reply.Err != nil {
		t.Fatalf("test reply: %+v", reply)
	}
}
