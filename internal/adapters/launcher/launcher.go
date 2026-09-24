package launcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
			path, err := childLookPath(req.Argv[0], l.env)
			if err != nil {
				l.log.Warn().Err(err).Str("binary", req.Argv[0]).Msg("spawn failed")
				continue
			}
			cmd := exec.Command(path, req.Argv[1:]...)
			cmd.Env = append(append([]string(nil), l.env...), req.Env...)
			cmd.Stdin = nil
			cmd.Stdout = nil
			cmd.Stderr = nil
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err != nil {
				l.log.Warn().Err(err).Str("binary", req.Argv[0]).Msg("spawn failed")
				continue
			}
			// Children have independent sessions and can outlive Run; reaping continues until process exit.
			go func() { l.log.Debug().Err(cmd.Wait()).Str("binary", req.Argv[0]).Msg("child exited") }()
		}
	}
}

func childLookPath(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	path := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			path = strings.TrimPrefix(entry, "PATH=")
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %q not found in child PATH", name)
}
