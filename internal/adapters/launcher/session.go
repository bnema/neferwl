package launcher

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"time"

	"bnema.dev/zerowrap"
)

// sessionVars are the displays D-Bus and systemd user services (portals,
// notification daemons) need to reach the session.
var sessionVars = []string{"WAYLAND_DISPLAY", "DISPLAY", "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE"}

// portalUnits are the desktop portal and its backends. They read the
// session variables once, at start: a portal started without them (before
// the export, or after the last session ended) never serves this session.
// Portals are per user, like the exported variables: another graphical
// session of the same user running at once is not supported.
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
// stops the portals that still hold them. When another session has exported
// its own display since (a desktop/server handover), the variables and the
// portals are its own: they are left alone.
func UnexportSession(env []string, log zerowrap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ours := envValue(env, "WAYLAND_DISPLAY")
	if current, ok := managerEnv(ctx, env, "WAYLAND_DISPLAY"); ok && current != "" && current != ours {
		log.Info().Str("ours", ours).Str("current", current).Msg("session unexport skipped: another session exported its display")
		return
	}
	runSystemctl(ctx, env, log, append([]string{"unset-environment"}, sessionVars...), "session unexport failed")
	runSystemctl(ctx, env, log, []string{"--no-block", "stop", portalUnits}, "portal stop failed")
}

// managerEnv reads key from the systemd user manager's environment ("" when
// unset); ok is false when it cannot be read.
func managerEnv(ctx context.Context, env []string, key string) (value string, ok bool) {
	path, err := childLookPath("systemctl", env)
	if err != nil {
		return "", false
	}
	cmd := exec.CommandContext(ctx, path, "--user", "show-environment")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if v, found := strings.CutPrefix(line, key+"="); found {
			return v, true
		}
	}
	return "", true
}

// envValue is key's value in env, "" when absent.
func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, found := strings.CutPrefix(env[i], key+"="); found {
			return v
		}
	}
	return ""
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
