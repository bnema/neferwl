package domain

// OutputMode describes a connected connector's physical mode.
type OutputMode struct {
	Width, Height, RefreshMilli int
	Preferred                   bool
}
