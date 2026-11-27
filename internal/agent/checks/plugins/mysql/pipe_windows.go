//go:build windows

package mysql

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// pipesSupported: a Windows agent reads a server on a named pipe.
const pipesSupported = true

// dialPipe connects to the named pipe at addr (\\.\pipe\<name>) under ctx;
// the driver passes its dial-timeout context.
func dialPipe(ctx context.Context, _, addr string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, addr)
}
