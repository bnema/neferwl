package screencast

import "github.com/bnema/neferwl/internal/ports"

func (s *Service) applyInventory(inv ports.CastInventory) { s.inv, s.haveInv = inv, true }
