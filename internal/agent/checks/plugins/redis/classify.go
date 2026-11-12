package redis

import (
	"errors"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// classify maps an error from connecting or reading to one fixed text. It
// never returns any part of err: a server's error reply can quote the user,
// a command or a setting, and a network error can name the host.
func classify(err error) string {
	var se *serverError
	if errors.As(err, &se) {
		return se.class
	}
	if errors.Is(err, errProtocol) {
		return "unexpected server response"
	}
	return dbcheck.Classify(err)
}
