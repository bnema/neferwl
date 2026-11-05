package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// lineageController gives tests real process branches, not synthetic PID hints.
// A is a session leader; B is its non-leader child and owns both probe branches.
func lineageController(stage string, direct bool) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if stage == "session" {
		args := []string{"-lineage-controller", "branch"}
		if direct {
			args = append(args, "-lineage-direct")
		}
		cmd := exec.Command(bin, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	start := func(args ...string) (*exec.Cmd, *os.File, error) {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, nil, err
		}
		cmd := exec.Command(bin, args...)
		cmd.Stdin = r
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			r.Close()
			w.Close()
			return nil, nil, err
		}
		r.Close()
		return cmd, w, nil
	}
	args := []string{"-class", "steam", "-initial-fullscreen", "-size", "640x360", "-focus-log", "-stdin-sync"}
	if direct {
		args = append(args, "-lineage-spawn")
	}
	p, pin, err := start(args...)
	if err != nil {
		return err
	}
	defer func() { pin.Close(); p.Process.Kill(); p.Wait() }()
	var g *exec.Cmd
	var gin *os.File
	defer func() {
		if g != nil {
			gin.Close()
			g.Process.Kill()
			g.Wait()
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if direct {
			fmt.Fprintln(pin, line)
			continue
		}
		switch line {
		case "spawn":
			if g == nil {
				g, gin, err = start("-class", "lineage-game", "-at", "9000,9000", "-focus-log", "-stdin-sync")
				if err != nil {
					return err
				}
			}
		case "close":
			if gin != nil {
				fmt.Fprintln(gin, "close")
			}
		default:
			fmt.Fprintln(pin, line)
			if gin != nil {
				fmt.Fprintln(gin, line)
			}
		}
	}
	return scanner.Err()
}

func startLineageSession(direct bool) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"-lineage-controller", "session"}
	if direct {
		args = append(args, "-lineage-direct")
	}
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
