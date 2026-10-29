// The uid boundary (backlog 2.42): a playbook task targeting `localhost`
// must not be able to read the worker's client certificate and key. The
// mechanism is a second uid in the worker image (pulsix-play, 65533, whose
// primary group is the worker's own 65532) plus the setuid-root helper
// /usr/local/bin/pulsix-setuid-exec that an unprivileged worker uses to
// reach it — see deploy/worker/setuid-exec.c and deploy/worker/Dockerfile.
//
// The boundary is a kernel one, so the only honest test is an integration
// test inside the real image: unit tests can prove the *exec.Cmd is shaped
// right (internal/worker/runner) and the directory modes are right
// (internal/worker), but neither can prove that uid 65533 is actually
// refused by the filesystem.
//
// What runs in the container is internal/e2e/uidboundary's test binary,
// compiled here for the image's platform and mounted in. It drives the
// REAL worker — worker.Loop with the real runner.Runner executor against a
// fake manager — through real jobs. The earlier version of this test
// staged the work dir by hand and invoked the helper directly, which is
// precisely why it stayed green while the shipped image could not run a
// single playbook: nothing it did went through the code that was broken.
package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envWorkerImage overrides the image under test; defaultWorkerImage is the
// tag `docker build -f deploy/worker/Dockerfile -t ...` produces locally.
// CI's docker job builds noixdev/pulsix-worker:<sha> and points this
// variable at it.
const (
	envWorkerImage     = "PULSIX_E2E_WORKER_IMAGE"
	defaultWorkerImage = "noixdev/pulsix-worker:dev"
	// envInImage switches the mounted binary out of skip mode; the
	// in-image package refuses to assert anything without it.
	envInImage = "PULSIX_UID_BOUNDARY_IN_IMAGE"
)

func TestPlaybookUIDBoundary(t *testing.T) {
	image := workerImageOrSkip(t)
	// A cold container running several whole ansible-playbook runs;
	// generous, and the test fails rather than hangs if the image
	// misbehaves.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	binary := buildInImageTest(ctx, t, image)

	// No --user: the image's own `USER 65532:65532` is part of what is
	// under test — the helper has to work for an unprivileged caller,
	// which is the whole reason it exists instead of CAP_SETUID on the
	// worker. --network none because the only network the tests need is
	// the container's own loopback (the SSH fixture binds 127.0.0.1).
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--network", "none",
		"-e", envInImage+"=1",
		"-v", binary+":/uid-boundary.test:ro",
		"--entrypoint", "/uid-boundary.test", image,
		"-test.v", "-test.timeout", "10m")
	out, err := cmd.CombinedOutput()
	t.Logf("docker run %s:\n%s", image, out)
	if err != nil {
		t.Fatalf("the uid boundary tests failed inside %s: %v", image, err)
	}
	// A binary that skipped everything would exit 0 too; every test skips
	// via inImage when the in-image marker is missing, so a skip means the
	// wiring here broke rather than the boundary holding. No test in the
	// package skips on purpose inside the image any more (backlog 2.107's
	// TestCancellingAPlayUnderThePlayUIDStopsIt was the last), so any skip
	// at all fails this.
	if strings.Contains(string(out), "--- SKIP") {
		t.Errorf("a test skipped inside the image; every in-image test must RUN there")
	}
}

// workerImageOrSkip returns the worker image to test, or skips.
func workerImageOrSkip(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker to run the worker image: %v", err)
	}
	image := os.Getenv(envWorkerImage)
	if image == "" {
		image = defaultWorkerImage
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("the pulsix worker image %q is not built here: build it with "+
			"`docker build -f deploy/worker/Dockerfile -t %s .` or point %s at an existing tag",
			image, defaultWorkerImage, envWorkerImage)
	}
	return image
}

// buildInImageTest compiles internal/e2e/uidboundary's test binary for the
// image's own platform. CGO is off so the binary runs on any libc; the
// architecture comes from the image itself rather than from runtime.GOARCH,
// because a developer's arm64 Docker and an amd64 image are a normal
// combination.
func buildInImageTest(ctx context.Context, t *testing.T, image string) string {
	t.Helper()
	arch, err := exec.CommandContext(ctx, "docker", "image", "inspect",
		"--format", "{{.Architecture}}", image).Output()
	if err != nil {
		t.Fatalf("read the image's architecture: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "uid-boundary.test")
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, "./uidboundary")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+strings.TrimSpace(string(arch)))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile the in-image boundary test: %v\n%s", err, out)
	}
	// The mount is read-only and the container runs as 65532: the binary
	// has to be readable and executable by anyone.
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatalf("chmod the test binary: %v", err)
	}
	return binary
}
