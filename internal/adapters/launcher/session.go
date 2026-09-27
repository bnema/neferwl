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

// ExportSession shares the session's displays with D-Bus and systemd user
// services, and waits for it: clients started next may activate them.
func ExportSession(ctx context.Context, env []string, log zerowrap.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	runSessionCommand(ctx, env, log, append([]string{"--systemd"}, sessionVars...), "session exported")
}

// UnexportSession withdraws the displays from the systemd user manager on
// exit, so services started after logout do not reach a dead display.
func UnexportSession(env []string, log zerowrap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path, err := childLookPath("systemctl", env)
	if err != nil {
		return
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"--user", "unset-environment"}, sessionVars...)...)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		log.Warn().Err(err).Msg("session unexport failed")
	}
}

func runSessionCommand(ctx context.Context, env []string, log zerowrap.Logger, args []string, done string) {
	path, err := childLookPath("dbus-update-activation-environment", env)
	if err != nil {
		log.Warn().Err(err).Msg("session export skipped: dbus-update-activation-environment not found")
		return
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		log.Warn().Err(err).Msg("session export failed")
		return
	}
	log.Info().Strs("vars", sessionVars).Msg(done)
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
