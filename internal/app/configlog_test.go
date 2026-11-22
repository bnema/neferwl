package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
)

// A config warning is logged by its component and never as a rejection;
// any other error is a rejection.
func TestLogConfigError(t *testing.T) {
	out := &lockedBuffer{}
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.New(zerowrap.Config{Format: "json", Level: "info", Output: out}))
	configLog := logging.For(ctx, "config")
	logConfigError(ctx, configLog, domain.ConfigWarning{Component: "drag", Msg: "drop.edge.left: no zone"})
	got := out.String()
	for _, want := range []string{`"level":"warn"`, `"component":"drag"`, `"message":"drop.edge.left: no zone"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning log lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, "config rejected") {
		t.Fatalf("warning logged as a rejection: %s", got)
	}
	logConfigError(ctx, configLog, errors.New("invalid gaps"))
	got = strings.TrimPrefix(out.String(), got)
	for _, want := range []string{`"component":"config"`, `"error":"invalid gaps"`, `"message":"config rejected"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("rejection log lacks %s: %s", want, got)
		}
	}
}
