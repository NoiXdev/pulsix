package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/setup"
)

// setupStatusReader is the part of *setup.Service logSetupToken reads.
type setupStatusReader interface {
	Status(ctx context.Context) (setup.Status, error)
}

// logSetupToken logs the setup token and the ready link to the wizard at
// WARN level while setup is not completed, so an operator who started the
// manager finds it in its log (`docker compose logs manager | grep -i
// "setup token"`). Once setup is completed nothing is logged: the token has
// no use any more. A status that cannot be read is logged without the
// token rather than failing the start.
func logSetupToken(ctx context.Context, log *slog.Logger, st setupStatusReader, token, publicURL string) {
	status, err := st.Status(ctx)
	if err != nil {
		log.WarnContext(ctx, "could not read the setup status; pulsix-manager setup-token prints the setup token", "err", err)
		return
	}
	if status.Completed {
		return
	}
	log.WarnContext(ctx, "setup is not completed: open the setup link, or enter the setup token on the wizard's first step",
		"setup_token", token, "setup_link", setup.Link(publicURL, token))
}

// setupTokenCheckTimeout bounds the setup-token command's look at the
// database: the token itself needs none, so an unreachable database only
// delays the answer this long.
const setupTokenCheckTimeout = 5 * time.Second

// RunSetupToken is `pulsix-manager setup-token`: it prints the setup token
// and the link that pre-fills it in the wizard. Both need only the master
// key and the public URL. When cfg names a database that is reachable and
// setup is already completed there, it says so instead; any failure to
// look (no database configured, unreachable, not migrated yet) still
// prints the token.
func RunSetupToken(ctx context.Context, cfg config.Config, stdout io.Writer) error {
	if setupCompleted(ctx, cfg) {
		_, err := fmt.Fprintln(stdout, "setup is already completed; no token is needed")
		return err
	}
	token := setup.Token(cfg.MasterKey)
	_, err := fmt.Fprintf(stdout, "Setup token: %s\nSetup link:  %s\n", token, setup.Link(cfg.PublicURL, token))
	return err
}

// setupCompleted reports whether cfg's database is reachable and has
// completed setup; false whenever it cannot tell.
func setupCompleted(ctx context.Context, cfg config.Config) bool {
	if cfg.DatabaseURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, setupTokenCheckTimeout)
	defer cancel()
	d, err := openForCommand(ctx, cfg)
	if err != nil {
		return false
	}
	defer d.Close()
	done, err := setup.NewSQLStore(d).AdminExists(ctx)
	return err == nil && done
}
