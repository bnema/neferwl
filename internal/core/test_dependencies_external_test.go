package core_test

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	systemclock "github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/core"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func testClock(t testing.TB) *portsmocks.MockClock {
	t.Helper()
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(time.Now).Maybe()
	clock.EXPECT().NewTimer(mock.Anything).RunAndReturn(func(d time.Duration) ports.Timer {
		real := (systemclock.System{}).NewTimer(d)
		timer := portsmocks.NewMockTimer(t)
		timer.EXPECT().C().Return(real.C()).Maybe()
		timer.EXPECT().Stop().RunAndReturn(real.Stop).Maybe()
		timer.EXPECT().Reset(mock.Anything).RunAndReturn(real.Reset).Maybe()
		return timer
	}).Maybe()
	return clock
}

func testOptions(t testing.TB, opts core.Options) core.Options {
	t.Helper()
	if opts.Clock == nil {
		opts.Clock = testClock(t)
	}
	if opts.Tokens == nil {
		tokens := portsmocks.NewMockTokenSource(t)
		var n atomic.Uint64
		tokens.EXPECT().Token().RunAndReturn(func() string { return strconv.FormatUint(n.Add(1), 10) }).Maybe()
		opts.Tokens = tokens
	}
	return opts
}
