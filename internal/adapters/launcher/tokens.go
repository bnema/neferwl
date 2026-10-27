package launcher

import "crypto/rand"

// Tokens mints cryptographically random spawn placement tokens.
type Tokens struct{}

func (Tokens) Token() string { return rand.Text() }
