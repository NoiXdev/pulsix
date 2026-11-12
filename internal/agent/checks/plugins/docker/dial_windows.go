//go:build windows

package docker

import (
	"context"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
)

// defaultDockerSocket is the named pipe Docker Desktop and the Windows engine
// serve on.
const defaultDockerSocket = "npipe:////./pipe/docker_engine"

const npipePrefix = "npipe:////./pipe/"

// dialDocker connects to the engine at socket. An npipe:////./pipe/<name>
// address is dialed as the named pipe \\.\pipe\<name>. Any other address (an
// absolute path, the second form the manager accepts) is dialed as a Unix
// socket, which Windows supports (AF_UNIX), so an engine exposed that way
// still works.
func dialDocker(ctx context.Context, socket string) (net.Conn, error) {
	if name, ok := strings.CutPrefix(socket, npipePrefix); ok {
		return winio.DialPipeContext(ctx, `\\.\pipe\`+name)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", socket)
}
