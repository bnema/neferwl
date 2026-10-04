// Command neferwl is the NeferWL Wayland compositor. The command line lives
// in internal/cli; the compositor wiring in internal/app.
package main

import (
	"os"

	"github.com/bnema/neferwl/internal/cli"
)

// version is set with -ldflags "-X main.version=..." by packaged builds.
var version string

func main() {
	os.Exit(cli.Main(os.Args[1:], version))
}
