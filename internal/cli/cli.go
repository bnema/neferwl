// Package cli parses the neferwl command line and runs the selected command.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// Env holds what a command uses besides its arguments.
type Env struct {
	Stdout, Stderr io.Writer
	// Version is the packaged build version; empty for source builds.
	Version string
}

// Runner runs a command with its positional arguments, after flag parsing.
type Runner func(ctx context.Context, env Env, args []string) error

// Command is one neferwl command. Setup registers the command's flags on fs
// and returns its Runner, which reads the parsed flag values.
type Command struct {
	Name    string // empty for the root command
	Args    string // positional arguments synopsis, e.g. "[path]"
	Summary string
	Setup   func(fs *flag.FlagSet) Runner
}

// root runs the compositor; commands are the subcommands.
var (
	root     = Command{Args: "[flags]", Summary: "Run the compositor.", Setup: setupRun}
	commands = []Command{validateConfigCmd, stateCmd, versionCmd}
)

// Main runs the command line args (without the program name) and returns
// the process exit code: 0 on success, 2 on usage errors, 1 otherwise.
func Main(args []string, version string) int {
	env := Env{Stdout: os.Stdout, Stderr: os.Stderr, Version: version}
	return exitCode(env.Stderr, Execute(context.Background(), env, args))
}

// Execute selects the command named by args[0], or the root command, parses
// its flags and runs it. -h and --help print the command usage to Stdout.
func Execute(ctx context.Context, env Env, args []string) error {
	cmd := lookup(args)
	if cmd.Name != "" {
		args = args[1:]
	}
	fs := flag.NewFlagSet(cmd.path(), flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors and usage are printed once, here and in exitCode
	run := cmd.Setup(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(env.Stdout, cmd, fs)
			return nil
		}
		return cmd.usageError(err)
	}
	err := run(ctx, env, fs.Args())
	var usage usageError
	if errors.As(err, &usage) {
		return cmd.usageError(usage.error)
	}
	return err
}

func lookup(args []string) Command {
	if len(args) > 0 {
		for _, c := range commands {
			if args[0] == c.Name {
				return c
			}
		}
	}
	return root
}

func (c Command) path() string {
	if c.Name == "" {
		return "neferwl"
	}
	return "neferwl " + c.Name
}

func (c Command) synopsis() string {
	if c.Args == "" {
		return c.path()
	}
	return c.path() + " " + c.Args
}

// usageError marks a bad command line; it exits with code 2.
type usageError struct{ error }

// usagef returns a usage error; Execute appends the command synopsis.
func usagef(format string, a ...any) error {
	return usageError{fmt.Errorf(format, a...)}
}

func (c Command) usageError(err error) error {
	return usageError{fmt.Errorf("%w\nusage: %s (see %s --help)", err, c.synopsis(), c.path())}
}

func printUsage(w io.Writer, cmd Command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "usage: %s\n\n%s\n", cmd.synopsis(), cmd.Summary)
	if cmd.Name == "" {
		fmt.Fprintln(w, "\nCommands:")
		for _, c := range commands {
			fmt.Fprintf(w, "  %-34s %s\n", c.synopsis(), c.Summary)
		}
	}
	hasFlags := false
	fs.VisitAll(func(*flag.Flag) { hasFlags = true })
	if hasFlags {
		fmt.Fprintln(w, "\nFlags:")
		fs.SetOutput(w)
		fs.PrintDefaults()
	}
}

// exitCode prints err to w, unless the run log already has it, and maps
// it to the process exit code. A canceled context (SIGINT, SIGTERM) is a
// clean exit.
func exitCode(w io.Writer, err error) int {
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	if !errors.As(err, new(loggedError)) {
		fmt.Fprintln(w, err)
	}
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}
