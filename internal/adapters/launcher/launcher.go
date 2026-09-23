package launcher

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"syscall"

	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
)

type Launcher struct {
	env []string
	log zerowrap.Logger
}

func New(env []string, log zerowrap.Logger) *Launcher {
	return &Launcher{env: append([]string(nil), env...), log: log}
}

func ChildEnv(base []string, waylandDisplay, runtimeDir string) []string {
	values := map[string]string{}
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch {
		case key == "HOME", key == "USER", key == "LOGNAME", key == "SHELL", key == "PATH", key == "LANG", strings.HasPrefix(key, "LC_"), strings.HasPrefix(key, "XDG_"):
			values[key] = value
		}
	}
	values["WAYLAND_DISPLAY"] = waylandDisplay
	values["XDG_RUNTIME_DIR"] = runtimeDir
	values["XDG_CURRENT_DESKTOP"] = "nefertty"
	values["XDG_SESSION_TYPE"] = "wayland"
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func (l *Launcher) Run(ctx context.Context, reqs <-chan ports.SpawnRequest) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case req, ok := <-reqs:
			if !ok {
				return nil
			}
			if len(req.Argv) == 0 {
				l.log.Warn().Msg("empty spawn argv")
				continue
			}
			path, err := exec.LookPath(req.Argv[0])
			if err != nil {
				l.log.Warn().Err(err).Str("binary", req.Argv[0]).Msg("spawn failed")
				continue
			}
			cmd := exec.Command(path, req.Argv[1:]...)
			cmd.Env = l.env
			cmd.Stdin = nil
			cmd.Stdout = nil
			cmd.Stderr = nil
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err != nil {
				l.log.Warn().Err(err).Str("binary", req.Argv[0]).Msg("spawn failed")
				continue
			}
			go func() { l.log.Debug().Err(cmd.Wait()).Str("binary", req.Argv[0]).Msg("child exited") }()
		}
	}
}
