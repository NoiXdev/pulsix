package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/config"
)

const setupTokenSynopsis = "pulsix-manager setup-token"

// dbURLPlaceholder stands in for an unset PULSIX_DATABASE_URL while
// setup-token loads its configuration: the token needs only the master key
// and the public URL, and config.Load would otherwise refuse to load.
const dbURLPlaceholder = "postgres://setup-token.invalid/unset"

// runSetupToken prints the setup token and the link that pre-fills it in
// the wizard (app.RunSetupToken).
func runSetupToken(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s\n", setupTokenSynopsis)
		_, _ = fmt.Fprint(stderr, "Prints the token the setup wizard asks for, from PULSIX_MASTER_KEY and PULSIX_PUBLIC_URL.\n")
	}
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() > 0 {
		return usageFailure(fs, stderr, fmt.Errorf("unexpected arguments %v", fs.Args()))
	}
	noDB := os.Getenv("PULSIX_DATABASE_URL") == ""
	cfg, err := config.Load(func(k string) string {
		if k == "PULSIX_DATABASE_URL" && noDB {
			return dbURLPlaceholder
		}
		return os.Getenv(k)
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "setup-token: configuration error: %v\n", err)
		return exitFailure
	}
	if noDB {
		cfg.DatabaseURL = ""
	}
	ctx, stop := signalContext()
	defer stop()
	return commandResult(fs, stderr, app.RunSetupToken(ctx, cfg, stdout))
}
