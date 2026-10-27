package ports

// TokenSource supplies cryptographically unguessable, unique placement tokens.
type TokenSource interface {
	Token() string
}
