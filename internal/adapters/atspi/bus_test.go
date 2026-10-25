package atspi

import (
	"bufio"
	"bytes"
	"context"
	"github.com/bnema/zerowrap"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bnema/zerobus"
	"github.com/stretchr/testify/require"
)

// startBus starts a private dbus-daemon, so the test never touches the
// desktop's buses. It skips when the daemon is not installed.
func startBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("dbus-daemon not installed")
		}
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--nopidfile", "--print-address=1")
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	checkStartup := boundIO(t, "dbus-daemon startup", func() { _ = cmd.Process.Kill(); _ = out.Close() })
	line, err := bufio.NewReader(out).ReadString('\n')
	checkStartup()
	require.NoError(t, err)
	return strings.TrimSpace(line)
}

// TestDoDecodesMatchesPipelined runs Do against an app on a real bus: the
// GetMatches reply is decoded, and replies to several calls sent before any
// is read are matched by serial.
func TestDoDecodesMatchesPipelined(t *testing.T) {
	addr := startBus(t)
	app, err := zerobus.Dial(addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close() })
	checkName := boundIO(t, "RequestName", func() { _ = app.Close() })
	_, err = app.RequestName("org.test.App", 0)
	require.NoError(t, err)
	checkName()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			m, err := app.ReadMessage()
			if err != nil {
				return
			}
			if m.Type != zerobus.TypeMethodCall {
				continue
			}
			req := &zerobus.Message{Sender: strings.Clone(m.Sender), Serial: m.Serial}
			switch m.Member {
			case "GetMatches":
				// Check the rule: struct, then sort, count, traverse.
				r := m.Body()
				r.Struct()
				end := r.Array('i')
				var states []int32
				for r.More(end) {
					states = append(states, r.Int32())
				}
				_ = r.Int32()
				ad := r.Array('{')
				_ = ad
				_ = r.Int32()
				end = r.Array('i')
				var roles []int32
				for r.More(end) {
					roles = append(roles, r.Int32())
				}
				ok := r.Err() == nil && len(states) == 2 && states[0] == 1<<25|1<<30 && len(roles) == 5 && uint32(roles[2]) == 1<<24|1<<18|1<<31
				if !ok {
					app.NewError(req, "org.test.Error.BadRule", "s").Str("bad rule")
					_, _ = app.Send()
					continue
				}
				e := app.NewReply(req, "a(so)")
				a := e.BeginArray('(')
				for _, p := range []string{"/one", "/two"} {
					e.Struct()
					e.Str(":1.99")
					e.ObjectPath(p)
				}
				e.EndArray(a)
			case "GetExtents":
				e := app.NewReply(req, "(iiii)")
				e.Struct()
				e.Int32(1)
				e.Int32(2)
				e.Int32(30)
				e.Int32(40)
			case "GetRole":
				app.NewReply(req, "u").Uint32(roleLink)
			case "GetState":
				e := app.NewReply(req, "au")
				a := e.BeginArray('u')
				e.Uint32(stShowing)
				e.Uint32(0)
				e.EndArray(a)
			default:
				app.NewError(req, "org.freedesktop.DBus.Error.UnknownMethod", "s").Str("no")
			}
			_, _ = app.Send()
		}
	}()

	cc, err := zerobus.Dial(addr)
	require.NoError(t, err)
	b, err := newConn(cc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	calls := []call{
		{op: opMatches, name: "org.test.App", path: "/frame", rule: matchRule{linksOnly: true}},
		{op: opExtents, name: "org.test.App", path: "/one", arg: coordWindow},
		{op: opRole, name: "org.test.App", path: "/one"},
		{op: opState, name: "org.test.App", path: "/one"},
		{op: opChildren, name: "org.test.App", path: "/one"},
	}
	rep := b.Do(time.Now().Add(2*time.Second), time.Second, calls)
	require.Len(t, rep, 5)
	require.NoError(t, rep[0].err)
	require.Equal(t, []ref{{":1.99", "/one"}, {":1.99", "/two"}}, rep[0].refs)
	require.NoError(t, rep[1].err)
	require.Equal(t, [4]int32{1, 2, 30, 40}, rep[1].rect)
	require.Equal(t, uint32(roleLink), rep[2].u)
	require.Equal(t, uint64(stShowing), rep[3].state)
	var be *busError
	require.ErrorAs(t, rep[4].err, &be)
	require.Equal(t, "org.freedesktop.DBus.Error.UnknownMethod", be.name)

	// A call to a name nobody owns fails fast, others still succeed.
	rep = b.Do(time.Now().Add(2*time.Second), time.Second, []call{
		{op: opRole, name: "org.test.Nobody", path: "/x"},
		{op: opRole, name: "org.test.App", path: "/x"},
	})
	require.Error(t, rep[0].err)
	require.NoError(t, rep[1].err)
}

func TestDoTimesOutOnSilentApp(t *testing.T) {
	addr := startBus(t)
	app, err := zerobus.Dial(addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close() })
	checkName := boundIO(t, "RequestName", func() { _ = app.Close() })
	_, err = app.RequestName("org.test.Mute", 0)
	require.NoError(t, err)
	checkName()

	cc, err := zerobus.Dial(addr)
	require.NoError(t, err)
	b, err := newConn(cc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	t0 := time.Now()
	rep := b.Do(time.Now().Add(time.Second), 50*time.Millisecond, []call{{op: opRole, name: "org.test.Mute", path: "/x"}})
	require.ErrorIs(t, rep[0].err, ErrTimeout)
	require.Less(t, time.Since(t0), 500*time.Millisecond)
}

// A call left unanswered for idle gets ErrTimeout without breaking the
// connection; a batch that hits its deadline breaks it, and Do returns near
// the deadline even though the idle timeout is longer.
func TestDoDeadlineBreaksConnection(t *testing.T) {
	addr := startBus(t)
	app, err := zerobus.Dial(addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close() })
	checkName := boundIO(t, "RequestName", func() { _ = app.Close() })
	_, err = app.RequestName("org.test.Mute", 0)
	require.NoError(t, err)
	checkName()

	cc, err := zerobus.Dial(addr)
	require.NoError(t, err)
	b, err := newConn(cc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })

	mute := []call{{op: opRole, name: "org.test.Mute", path: "/x"}}
	rep := b.Do(time.Now().Add(time.Second), 40*time.Millisecond, mute)
	require.ErrorIs(t, rep[0].err, ErrTimeout)
	require.NotErrorIs(t, rep[0].err, ErrBroken)

	t0 := time.Now()
	rep = b.Do(time.Now().Add(60*time.Millisecond), time.Second, mute)
	require.ErrorIs(t, rep[0].err, ErrBroken)
	require.Less(t, time.Since(t0), 300*time.Millisecond)
	// The socket is closed: the next batch fails at once.
	rep = b.Do(time.Now().Add(time.Second), time.Second, mute)
	require.ErrorIs(t, rep[0].err, ErrBroken)
}

// Cancel while IsEnabled is waiting on a real service. Its failed Call is
// shutdown, whereas expiry of the connector's own budget must still warn.
func TestConnectCancelledWhileEnabling(t *testing.T) {
	address := startBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
	app, err := zerobus.Dial(address)
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close() })
	check := boundIO(t, "own accessibility name", func() { _ = app.Close() })
	_, err = app.RequestName(a11yBusName, 0)
	check()
	require.NoError(t, err)
	var output bytes.Buffer // read only after Connect has returned
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &output}))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	peerDone := make(chan error, 1)
	stopPeer := context.AfterFunc(ctx, func() { _ = app.Close() })
	defer stopPeer()
	go func() {
		for {
			m, err := app.ReadMessage()
			if err != nil {
				peerDone <- err
				return
			}
			if m.Type != zerobus.TypeMethodCall {
				continue
			}
			if m.Member == "GetAddress" {
				app.NewReply(m, "s").Str(address)
				if _, err := app.Send(); err != nil {
					peerDone <- err
					return
				}
			} else if m.Member == "Set" {
				cancel()
				peerDone <- nil
				return
			}
		}
	}()
	tr, err := (busConnector{}).Connect(ctx)
	require.Error(t, err)
	require.Nil(t, tr)
	select {
	case err := <-peerDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("accessibility peer did not stop")
	}
	require.NotContains(t, output.String(), "could not enable accessibility")
}
