package core

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestDiffConfigReload(t *testing.T) {
	cur := domain.Config{}
	cur.Hint.Sources = []domain.HintSource{1}
	next := cur
	next.Render.Device = "card1"
	next.Hint.Enable = !cur.Hint.Enable
	next.Keyboard.Layout = "fr"
	next.Keyboard.RepeatRate++
	p := DiffConfigReload(cur, next)
	if len(p.RestartOnly) != 4 || !p.HintRestartOnly || !p.KeyboardLayoutChanged || !p.RepeatChanged {
		t.Fatalf("missing decisions: %+v", p)
	}
	if p.Sanitized.Hint.Enable != cur.Hint.Enable || p.Sanitized.Keyboard.Layout != "fr" || p.Sanitized.Render.Device != "card1" {
		t.Fatalf("unexpected sanitization: %+v", p.Sanitized)
	}
	p.Sanitized.Hint.Sources[0] = 2
	if cur.Hint.Sources[0] != 1 {
		t.Fatal("sanitized sources alias baseline")
	}
	same := DiffConfigReload(cur, cur)
	if len(same.RestartOnly) != 0 || same.HintRestartOnly || same.KeyboardLayoutChanged || same.RepeatChanged {
		t.Fatalf("unchanged: %+v", same)
	}
}
