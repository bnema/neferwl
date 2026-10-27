package core

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestNewRequiresDependencies(t *testing.T) {
	ch := Channels{Scenes: make(chan []domain.Scene, 1)}
	opts := testOptions(t, Options{})
	opts.Clock = nil
	if _, err := New(domain.Config{}, ch, opts); err == nil {
		t.Fatal("missing clock accepted")
	}
	opts = testOptions(t, Options{})
	opts.Tokens = nil
	if _, err := New(domain.Config{}, ch, opts); err == nil {
		t.Fatal("missing tokens accepted")
	}
}
