package launcher

import (
	"context"
	"net"
	"os/exec"
	"time"

	"github.com/bnema/zerowrap"
)

// sessionVars are the displays D-Bus and systemd user services (portals,
// notification daemons) need to reach the session.
var sessionVars = []string{"WAYLAND_DISPLAY", "DISPLAY", "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE"}

// portalUnits are the desktop portal and its backends. They read the
// session variables once, at start: a portal started without them (before
// the export, or after the last session ended) never serves this session.
const portalUnits = "xdg-desktop-portal*.service"

// ExportSession shares the session's displays with D-Bus and systemd user
// services, and waits for it: clients started next may activate them. Portals
// already running restart to read them; the restart does not block, since a
// backend may connect to the display before it serves.
func ExportSession(ctx context.Context, env []string, log zerowrap.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !runSessionCommand(ctx, env, log, append([]string{"--systemd"}, sessionVars...), "session exported") {
		return
	}
	runSystemctl(ctx, env, log, []string{"--no-block", "try-restart", portalUnits}, "portal restart failed")
}

// UnexportSession withdraws the displays from the systemd user manager on
// exit, so services started after logout do not reach a dead display, and
// stops the portals that still hold them.
func UnexportSession(env []string, log zerowrap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runSystemctl(ctx, env, log, append([]string{"unset-environment"}, sessionVars...), "session unexport failed")
	runSystemctl(ctx, env, log, []string{"--no-block", "stop", portalUnits}, "portal stop failed")
}

// runSystemctl runs systemctl --user with args; a missing systemctl is skipped.
func runSystemctl(ctx context.Context, env []string, log zerowrap.Logger, args []string, failed string) {
	path, err := childLookPath("systemctl", env)
	if err != nil {
		return
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"--user"}, args...)...)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		log.Warn().Err(err).Strs("args", args).Msg(failed)
	}
}

// runSessionCommand runs dbus-update-activation-environment and reports
// whether it succeeded.
func runSessionCommand(ctx context.Context, env []string, log zerowrap.Logger, args []string, done string) bool {
	path, err := childLookPath("dbus-update-activation-environment", env)
	if err != nil {
		log.Warn().Err(err).Msg("session export skipped: dbus-update-activation-environment not found")
		return false
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		log.Warn().Err(err).Msg("session export failed")
		return false
	}
	log.Info().Strs("vars", sessionVars).Msg(done)
	return true
}

// NotifyReady tells the systemd user manager that the compositor is ready.
func NotifyReady(socket string, log zerowrap.Logger) {
	if socket == "" {
		return
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		log.Warn().Err(err).Msg("session readiness notification failed")
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("READY=1")); err != nil {
		log.Warn().Err(err).Msg("session readiness notification failed")
		return
	}
	log.Info().Msg("session ready")
}
