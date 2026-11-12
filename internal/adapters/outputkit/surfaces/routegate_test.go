package surfaces

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRouteGate(t *testing.T) {
	t0 := time.Unix(100, 0)
	var g RouteGate
	_, wait := g.Wait(t0)
	require.False(t, wait, "zero gate never waits")
	g.Scene(0, t0)
	_, wait = g.Wait(t0)
	require.False(t, wait, "Routes=0 never gates")

	g.Scene(2, t0)
	d, wait := g.Wait(t0)
	require.True(t, wait)
	require.Equal(t, routeWait, d)
	g.Scene(2, t0.Add(40*time.Millisecond)) // the same need keeps the deadline
	d, wait = g.Wait(t0.Add(40 * time.Millisecond))
	require.True(t, wait)
	require.Equal(t, 60*time.Millisecond, d)
	g.Batch(1)
	_, wait = g.Wait(t0)
	require.True(t, wait, "older generation does not release")
	g.Batch(2)
	_, wait = g.Wait(t0)
	require.False(t, wait)
	require.False(t, g.Late(t0.Add(time.Hour)))

	g.Scene(3, t0)
	_, wait = g.Wait(t0.Add(routeWait))
	require.False(t, wait, "renders after routeWait")
	require.False(t, g.Late(t0.Add(routeWait-1)))
	require.True(t, g.Late(t0.Add(routeWait)))
	require.False(t, g.Late(t0.Add(routeWait)), "one report per expiry")
	g.Scene(3, t0.Add(time.Second)) // the same need does not re-arm
	_, wait = g.Wait(t0.Add(time.Second))
	require.False(t, wait)
	g.Batch(3)
	g.Scene(2, t0) // an older need never gates
	_, wait = g.Wait(t0)
	require.False(t, wait)
}
