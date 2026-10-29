package api

import (
	"fmt"
	"strings"
)

// WorkerEndpoint is where remote workers reach the manager: Addr is
// PULSIX_GRPC_PUBLIC_ADDR (empty: the worker endpoint is disabled) and
// ServerName the name the manager's certificate carries.
type WorkerEndpoint struct {
	Addr       string
	ServerName string
}

// workerSnippets renders the docker run and compose snippets the Add
// worker dialog shows next to the token. Both start the worker under an init
// process (--init, init: true): the worker is otherwise PID 1 and never
// reaps what a run orphans outside its per-run supervisor (backlog 2.108).
// The bundled compose file sets the same.
func workerSnippets(ep WorkerEndpoint, name, token, version string) (docker, compose string) {
	if version == "" || version == "dev" {
		version = "latest"
	}
	docker = strings.Join([]string{
		fmt.Sprintf("docker run -d --init --name pulsix-worker-%s --restart unless-stopped \\", name),
		fmt.Sprintf("  -e PULSIX_MANAGER_ADDR=%s \\", ep.Addr),
		fmt.Sprintf("  -e PULSIX_MANAGER_SERVER_NAME=%s \\", ep.ServerName),
		fmt.Sprintf("  -e PULSIX_ENROLL_TOKEN=%s \\", token),
		fmt.Sprintf("  -v pulsix-worker-%s:/var/lib/pulsix-worker \\", name),
		fmt.Sprintf("  noixdev/pulsix-worker:%s", version),
	}, "\n")
	compose = strings.Join([]string{
		"services:",
		fmt.Sprintf("  pulsix-worker-%s:", name),
		fmt.Sprintf("    image: noixdev/pulsix-worker:%s", version),
		"    init: true",
		"    restart: unless-stopped",
		"    environment:",
		fmt.Sprintf("      PULSIX_MANAGER_ADDR: %q", ep.Addr),
		fmt.Sprintf("      PULSIX_MANAGER_SERVER_NAME: %q", ep.ServerName),
		fmt.Sprintf("      PULSIX_ENROLL_TOKEN: %q", token),
		"    volumes:",
		fmt.Sprintf("      - pulsix-worker-%s:/var/lib/pulsix-worker", name),
		"volumes:",
		fmt.Sprintf("  pulsix-worker-%s: {}", name),
	}, "\n")
	return docker, compose
}
