// unifictl makes the console match the files; the command is internal/cli.
package main

import (
	"context"
	"os"

	"github.com/mycophonic/primordium/app"
	"github.com/mycophonic/primordium/app/shutdown"

	"github.com/duncanite/go-unifi/internal/cli"
)

func main() {
	// Main runs the shutdown handlers however run ends and exits with the
	// status an ExitError asks for, silently: every message is printed.
	app.Main(&app.Options{Name: "unifictl"}, func(ctx context.Context) error {
		if code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); code != cli.ExitOK {
			return shutdown.ExitError(code)
		}

		return nil
	})
}
