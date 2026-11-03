package headless

import (
	"context"

	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// StartOutput registers an owner in the backend's existing set and announces
// it before RunOutput can emit capabilities or render. The caller owns cursor
// registration and lifetime removal. False means startup or announcement failed;
// the caller must finish/wait the set as appropriate.
func StartOutput(ctx context.Context, set *routing.Set, ch routing.Channels, info domain.OutputInfo, opts Options) bool {
	announced := make(chan struct{})
	if !set.Start(ctx, info.Name, func(octx context.Context, feed routing.Feed) error {
		select {
		case <-announced:
		case <-octx.Done():
			return nil
		}
		opts.Security, opts.SecurityChanges, opts.Instance = ch.Security, feed.Security, feed.Instance
		return RunOutput(octx, opts, feed.Scenes, feed.Contents, feed.Returns, feed.Cursor, feed.Captures)
	}, &opts.SecurityEvents) {
		return false
	}
	select {
	case ch.Events <- ports.OutputAdded{Info: info}:
		close(announced)
		return true
	case <-ctx.Done():
		return false
	}
}
