//go:build !windows

package checks

import (
	"context"
	"errors"
	"net"
	"strings"
)

// defaultDockerSocket is where the Docker Engine listens on Linux and macOS.
const defaultDockerSocket = "/var/run/docker.sock"

// dialDocker connects to the engine at socket, an absolute path. An npipe
// address names a Windows named pipe and cannot be dialed here.
func dialDocker(ctx context.Context, socket string) (net.Conn, error) {
	if strings.HasPrefix(socket, "npipe:") {
		return nil, errors.New("named pipes are only available on Windows")
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", socket)
}
