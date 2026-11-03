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
	docker, compose, _ := workerSnippets(ep, "hel-1", "pxw1.token", "v0.5.0")

	if !strings.Contains(docker, "docker run -d --init --name pulsix-worker-hel-1 ") {
		t.Errorf("docker snippet must run `docker run -d --init --name ...`, got:\n%s", docker)
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

// TestWorkerSnippetsSeparateThePlayUID is backlog 7.32: a worker started
// from a pasted snippet is a new installation on the official image and a
// fresh volume, so the reason the image leaves PULSIX_WORKER_PLAY_UID
// unset (not to opt an existing worker in at its next pull) does not
// apply. Without it the playbooks run as the worker and can read its key.
func TestWorkerSnippetsSeparateThePlayUID(t *testing.T) {
	ep := WorkerEndpoint{Addr: "pulsix.example.com:8444", ServerName: "pulsix-manager"}
	docker, compose, _ := workerSnippets(ep, "hel-1", "pxw1.token", "v0.5.0")

	if !strings.Contains(docker, "  -e PULSIX_WORKER_PLAY_UID=65533 \\\n") {
		t.Errorf("docker snippet does not set the play uid:\n%s", docker)
	}
	if !strings.Contains(compose, "      PULSIX_WORKER_PLAY_UID: \"65533\"\n") {
		t.Errorf("compose snippet does not set the play uid:\n%s", compose)
	}
}

// TestWorkerDockerSnippetKeepsTheTokenOutOfDockersArguments is backlog
// 7.39: as a `-e NAME=value` argument the enrollment token sits in the
// docker client's command line, which every local user of the host can
// read from the process list. The snippet hands it over in the client's
// environment instead, which only its own user and root can read.
func TestWorkerDockerSnippetKeepsTheTokenOutOfDockersArguments(t *testing.T) {
	ep := WorkerEndpoint{Addr: "pulsix.example.com:8444", ServerName: "pulsix-manager"}
	docker, _, _ := workerSnippets(ep, "hel-1", "pxw1.token", "v0.5.0")

	if !strings.HasPrefix(docker, "PULSIX_ENROLL_TOKEN=pxw1.token docker run ") {
		t.Errorf("the token must be the docker client's environment, set ahead of the command:\n%s", docker)
	}
	if strings.Contains(docker, "-e PULSIX_ENROLL_TOKEN=") || !strings.Contains(docker, "  -e PULSIX_ENROLL_TOKEN \\\n") {
		t.Errorf("the container must take the token from that environment, not from an argument:\n%s", docker)
	}
}

// TestWorkerComposeSnippetKeepsTheTokenOutOfTheFile is backlog 7.59: a
// compose file is saved, committed and shared, so a literal token in it is
// a credential at rest. The file only names the variable (and refuses to
// start without it); the token travels in the separate start command,
// which sets it as the compose client's environment like the docker
// snippet does.
func TestWorkerComposeSnippetKeepsTheTokenOutOfTheFile(t *testing.T) {
	ep := WorkerEndpoint{Addr: "pulsix.example.com:8444", ServerName: "pulsix-manager"}
	docker, compose, up := workerSnippets(ep, "hel-1", "pxw1.token", "v0.5.0")

	if strings.Contains(compose, "pxw1.token") {
		t.Errorf("the compose snippet must not contain the token:\n%s", compose)
	}
	want := "      PULSIX_ENROLL_TOKEN: ${PULSIX_ENROLL_TOKEN:?set PULSIX_ENROLL_TOKEN}\n"
	if !strings.Contains(compose, want) {
		t.Errorf("compose snippet must take the token from the environment, missing %q:\n%s", want, compose)
	}
	if up != "PULSIX_ENROLL_TOKEN=pxw1.token docker compose up -d" {
		t.Errorf("compose up command = %q", up)
	}
	// The docker snippet keeps the token as its client's environment.
	if !strings.HasPrefix(docker, "PULSIX_ENROLL_TOKEN=pxw1.token docker run ") {
		t.Errorf("docker snippet changed:\n%s", docker)
	}
}
