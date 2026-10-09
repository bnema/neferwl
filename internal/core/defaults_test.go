package core_test

import (
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/ports"
)

// scrollDefaults is the default config with scroll overflow, the layout most
// core tests were written against. Cascade has its own tests.
func scrollDefaults() ports.Config {
	cfg := config.Defaults()
	cfg.Layout.Overflow = "scroll"
	return cfg
}
