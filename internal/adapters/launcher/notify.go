package launcher

import (
	"context"
	"os/exec"
	"time"

	"github.com/bnema/zerowrap"
)

// Notifier shows desktop notifications with notify-send, so any
// notification daemon of the session (mako, dunst…) displays them.
type Notifier struct {
	env []string
	log zerowrap.Logger
}

// NewNotifier runs notify-send with the child environment, which carries
// the session bus address.
func NewNotifier(env []string, log zerowrap.Logger) *Notifier {
	return &Notifier{env: append([]string(nil), env...), log: log}
}

// Notify starts notify-send and returns; it is killed after five seconds.
// A missing tool or daemon only logs a warning.
func (n *Notifier) Notify(summary, body string) {
	path, err := childLookPath("notify-send", n.env)
	if err != nil {
		n.log.Warn().Err(err).Str("summary", summary).Msg("notification skipped: notify-send not found")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "--app-name=neferwl", "--", summary, body)
		cmd.Env = n.env
		if out, err := cmd.CombinedOutput(); err != nil {
			n.log.Warn().Err(err).Str("output", string(out)).Str("summary", summary).Msg("notification failed")
		}
	}()
}
