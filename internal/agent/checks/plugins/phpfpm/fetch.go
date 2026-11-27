package phpfpm

import (
	"context"
	"errors"
	"io/fs"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/fastcgi"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
	"github.com/noixdev/pulsix/internal/checkplugins"
)

// reasonInvalid is a FastCGI entry that does not parse; the manager's
// validation and the agent's local-check validation refuse it, so only a
// configuration that bypassed both gets here.
const reasonInvalid = "invalid address"

// fetcher is how entry e's page is read: an entry in a FastCGI form
// (backlog 3.11a) over FastCGI from the pool's own address, any other as
// a URL with the query key json added when missing.
func fetcher(e string) webcheck.Fetcher {
	if !checkplugins.IsPHPFPMFastCGI(e) {
		u := webcheck.WithQueryKey(e, "json")
		return func(ctx context.Context, opt webcheck.Options) ([]byte, string) { return webcheck.Fetch(ctx, u, opt) }
	}
	a, err := checkplugins.ParsePHPFPMFastCGI(e)
	if err != nil {
		return func(context.Context, webcheck.Options) ([]byte, string) { return nil, reasonInvalid }
	}
	return func(ctx context.Context, _ webcheck.Options) ([]byte, string) { return fetchFastCGI(ctx, a) }
}

// fetchFastCGI asks the pool for its status page as json under
// dbcheck.QueryTimeout, like an HTTP fetch. The secret (HTTP basic
// authentication) and verify_tls have nothing to apply to here.
func fetchFastCGI(ctx context.Context, a checkplugins.PHPFPMFastCGI) ([]byte, string) {
	ctx, cancel := context.WithTimeout(ctx, dbcheck.QueryTimeout)
	defer cancel()
	resp, err := fastcgi.Get(ctx, a.Network, a.Address, a.Path, "json", maxPage)
	if err != nil {
		return nil, fastcgiReason(err)
	}
	if reason := webcheck.StatusReason(resp.Status); reason != "" {
		return nil, reason
	}
	return resp.Body, ""
}

// fastcgiReason is the fixed text of a FastCGI failure: the client's own
// errors as they are, a socket that is missing or may not be opened by
// name, anything else through dbcheck.Classify. Never the address.
func fastcgiReason(err error) string {
	for _, known := range []error{fastcgi.ErrTooLarge, fastcgi.ErrProtocol, fastcgi.ErrRejected} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "socket not found"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	}
	return dbcheck.Classify(err)
}
