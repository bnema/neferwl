package core_test

import (
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/domain"
)

// scrollDefaults is the default config with scroll overflow, the layout most
// core tests were written against, with instant layout changes. Cascade and
// animations have their own tests.
func scrollDefaults() domain.Config {
	cfg := config.Defaults()
	cfg.Layout.Overflow = "scroll"
	cfg.Animations.On = false
	return cfg
}
