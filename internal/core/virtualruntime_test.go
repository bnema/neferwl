package core

import (
	"errors"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestRuntimeVirtualTransactions(t *testing.T) {
	file := domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "640x480"}}}
	a := NewOutputApply(NewOutputOverrides(file, true))
	mode := domain.OutputMode{Width: 1280, Height: 720, RefreshMilli: 60000}
	name, err := a.RuntimeAdd(10, "", mode)
	if err != nil || name != "HEADLESS-1" {
		t.Fatalf("add %q %v", name, err)
	}
	if cfg, ok := a.Next(); !ok || len(cfg.Outputs) != 2 {
		t.Fatalf("desired: %+v %v", cfg, ok)
	}
	if ch, _ := a.ReplyOut(make(chan domain.OutputApplied)); ch != nil {
		t.Fatal("reply before reconciliation")
	}
	a.Finished(nil)
	if _, reply := a.ReplyOut(make(chan domain.OutputApplied)); reply.ID != 10 || reply.Err != nil {
		t.Fatal(reply)
	}
	a.ReplySent()
	a.Reload(domain.Config{Outputs: []domain.OutputConfig{{Name: "other", Virtual: true, Mode: "800x600"}}})
	if cfg, ok := a.Next(); !ok || len(cfg.Outputs) != 2 || cfg.Outputs[1].Name != name {
		t.Fatalf("reload dropped runtime: %+v", cfg)
	}
	a.Finished(nil)
	if err := a.RuntimeRemove(11, "other"); !errors.Is(err, ErrRuntimeOutputForbidden) {
		t.Fatal(err)
	}
	if err := a.RuntimeRemove(12, name); err != nil {
		t.Fatal(err)
	}
	if cfg, ok := a.Next(); !ok || len(cfg.Outputs) != 1 {
		t.Fatalf("remove: %+v", cfg)
	}
	failure := errors.New("backend failed")
	a.Finished(failure)
	if cfg, ok := a.Next(); !ok || len(cfg.Outputs) != 2 {
		t.Fatalf("rollback: %+v", cfg)
	}
	a.Finished(nil)
	if _, reply := a.ReplyOut(make(chan domain.OutputApplied)); !errors.Is(reply.Err, failure) {
		t.Fatal(reply)
	}
	a.ReplySent()
	if err := a.RuntimeRemove(13, name); err != nil {
		t.Fatal(err)
	}
	a.Next()
	a.Finished(nil)
	a.ReplySent()
	if len(a.Virtuals()) != 1 {
		t.Fatal(a.Virtuals())
	}
	// A failed addition restores the desired set too.
	if _, err := a.RuntimeAdd(14, name, mode); err != nil {
		t.Fatal(err)
	}
	a.Next()
	a.Finished(failure)
	a.Next()
	a.Finished(nil)
	if len(a.Virtuals()) != 1 {
		t.Fatal(a.Virtuals())
	}
}
