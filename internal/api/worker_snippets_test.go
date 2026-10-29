package api

import (
	"strings"
	"testing"
)

// The worker's own process cannot reap what a run orphans outside the
// per-run supervisor's tree (a repository sync's ssh, a supervisor killed as
// the backstop), so a worker started from a pasted snippet needs an init
// process just like the bundled compose service has (backlog 2.108).
func TestWorkerSnippetsRunTheWorkerUnderAnInit(t *testing.T) {
	ep := WorkerEndpoint{Addr: "pulsix.example.com:8444", ServerName: "pulsix-manager"}
	docker, compose := workerSnippets(ep, "hel-1", "pxw1.token", "v0.5.0")

	if !strings.HasPrefix(docker, "docker run -d --init --name pulsix-worker-hel-1 ") {
		t.Errorf("docker snippet must start with `docker run -d --init --name ...`, got:\n%s", docker)
	}
	for _, want := range []string{
		"    image: noixdev/pulsix-worker:v0.5.0\n    init: true\n",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose snippet missing %q:\n%s", want, compose)
		}
	}
	// The rest of the snippet is what an operator already relies on.
	for _, want := range []string{"PULSIX_ENROLL_TOKEN=pxw1.token", "--restart unless-stopped", "noixdev/pulsix-worker:v0.5.0"} {
		if !strings.Contains(docker, want) {
			t.Errorf("docker snippet missing %q:\n%s", want, docker)
		}
	}
}
