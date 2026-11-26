//go:build !windows

package mysql

import (
	"context"
	"errors"
	"net"
)

// pipesSupported: named pipes exist on Windows only; parseSettings refuses
// the pipe field here, so dialPipe is never reached.
const pipesSupported = false

func dialPipe(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("named pipes are only available on Windows")
}
