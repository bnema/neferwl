package ports

import "github.com/bnema/neferwl/internal/domain"

// ContentReturn gives a consumed batch back to wayland. Return never blocks
// and is safe after the output was detached.
type ContentReturn interface{ Return(*domain.ContentBatch) }
