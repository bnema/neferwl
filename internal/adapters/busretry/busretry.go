// Package busretry keeps a D-Bus service running across bus restarts and
// busy names: it runs an attempt again after a growing delay until the
// context ends.
package busretry

import (
	"context"
	"errors"
	"time"

	"github.com/bnema/zerowrap"
)

// Max bounds the delay between attempts.
const Max = time.Minute

// Run calls attempt until ctx ends. When attempt returns, it waits retry,
// doubling up to Max on each failure, and calls it again. An attempt that
// ran longer than Max before failing starts the delay over: its failure is
// new, not a retry streak. what names the feature in logs ("X ignored until
// the next attempt"); a failure is warned once per distinct error, then
// logged at debug level.
func Run(ctx context.Context, retry time.Duration, what string, log zerowrap.Logger, attempt func(context.Context) error) {
	delay := retry
	var last string
	for {
		start := time.Now()
		err := attempt(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New(what + " stopped")
		}
		if time.Since(start) > Max {
			delay, last = retry, ""
		}
		if msg := err.Error(); msg != last {
			last = msg
			log.Warn().Err(err).Dur("retry", delay).Msg(what + " ignored until the next attempt")
		} else {
			log.Debug().Err(err).Dur("retry", delay).Msg(what + " still ignored")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, Max)
	}
}
