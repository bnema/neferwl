package core

import (
	"context"
	"testing"
	"testing/synctest"
)

// A font file loaded again (a failed load retried) changes only the
// generation the app forwards: the scene changes, so outputs redraw.
func TestFontGenChangeMakesNewScene(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		ctx := context.Background()
		before, err := c.sceneFor(ctx, 0, c.screens[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cfg := c.cfg
		cfg.FontGen++
		if err := c.apply(cfg); err != nil {
			t.Fatal(err)
		}
		after, err := c.sceneFor(ctx, 0, c.screens[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if after.FontGen != cfg.FontGen || before.SameAs(after) {
			t.Fatalf("font generation %d not in the scene: %d", cfg.FontGen, after.FontGen)
		}
	})
}
