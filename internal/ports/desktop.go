package ports

// Notifier shows desktop notifications (e.g. through mako). Notify does not
// block; delivery is best effort and implementations log their own failures.
type Notifier interface {
	Notify(summary, body string)
}

// OutputScaleStore persists output scales so they survive a restart.
type OutputScaleStore interface {
	// SaveOutputScale records scale as the configured scale of the output.
	SaveOutputScale(output string, scale float64) error
}
