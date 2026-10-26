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
// refused by the filesystem. This test therefore runs the image.
package e2e

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// envWorkerImage overrides the image under test; defaultWorkerImage is the
// tag `docker build -f deploy/worker/Dockerfile -t ...` and CI's release
// smoke test both produce.
const (
	envWorkerImage     = "PULSIX_E2E_WORKER_IMAGE"
	defaultWorkerImage = "noixdev/pulsix-worker:dev"
)

// The markers the in-container script prints; each stands for one half of
// the boundary. They are asserted by presence rather than by the script's
// exit code alone so a script that silently stopped early cannot pass.
const (
	markerBundleDirPrivate = "PULSIX-UID-TEST: bundle dir is 0700"
	markerKeyUnreadable    = "PULSIX-UID-TEST: the play cannot read the worker key"
	markerArtifactReadBack = "PULSIX-UID-TEST: the worker read back what the play wrote"
)

func TestPlaybookCannotReadTheWorkerKey(t *testing.T) {
	image := workerImageOrSkip(t)
	// A whole ansible-playbook run inside a cold container; generous, and
	// the test fails rather than hangs if the image misbehaves.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// No --user: the image's own `USER 65532:65532` is part of what is
	// under test — the helper has to work for an unprivileged caller, which
	// is the whole reason it exists instead of CAP_SETUID on the worker.
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"--entrypoint", "/bin/sh", image, "-c", uidBoundaryScript)
	out, err := cmd.CombinedOutput()
	t.Logf("docker run %s:\n%s", image, out)
	if err != nil {
		t.Fatalf("the uid boundary script failed: %v", err)
	}
	for _, marker := range []string{markerBundleDirPrivate, markerKeyUnreadable, markerArtifactReadBack} {
		if !strings.Contains(string(out), marker) {
			t.Errorf("the script never reported %q", marker)
		}
	}
}

// workerImageOrSkip returns the worker image to test, or skips. It mirrors
// web/e2e/worker.ts's probe: the image carrying the Ansible runtime is not
// part of the test stack yet, so an absent image is a skip with a visible
// reason and never a silent pass.
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
		t.Skipf("the pulsix worker image %q is not built here (override with %s): "+
			"backlog 2.96 is the blocker — the Ansible worker image is not in the test stack yet", image, envWorkerImage)
	}
	return image
}

// uidBoundaryScript runs as the image's own unprivileged user (65532). It
// stages what a real run stages — a certificate bundle in /etc/pulsix-worker
// and a group-shared work dir, the shapes deploy/worker/Dockerfile and
// internal/worker/runner.ShareWorkDir produce — then starts one play through
// the setuid helper and checks both directions of the boundary:
// the play cannot read the key, and the worker can read what the play wrote.
const uidBoundaryScript = `
set -eu
play_uid=65533
bundle=/etc/pulsix-worker
key=$bundle/key.pem
data=/var/lib/pulsix-worker
work=$data/uid-boundary

# The image's own shape, before any worker code has run: a bundle directory
# a localhost play could list is a key a localhost play can read.
mode=$(stat -c %a "$bundle")
if [ "$mode" != "700" ]; then
	echo "FAIL: $bundle is mode $mode, want 700"
	exit 1
fi
echo "PULSIX-UID-TEST: bundle dir is 0700"

printf 'not a real private key\n' > "$key"
chmod 0600 "$key"

# What the worker does at run time: the data dir traversable but not
# listable (0710), the work dir and everything in it group-shared and
# setgid so the play's own files stay group-owned by the worker.
mkdir -p "$work/project" "$work/home" "$work/tmp"
chmod 0710 "$data"
chmod 2770 "$work" "$work/project" "$work/home" "$work/tmp"
HOME=$work/home
ANSIBLE_HOME=$work/home/ansible
ANSIBLE_LOCAL_TEMP=$work/tmp
ANSIBLE_REMOTE_TEMP=$work/tmp
export HOME ANSIBLE_HOME ANSIBLE_LOCAL_TEMP ANSIBLE_REMOTE_TEMP

cat > "$work/project/play.yml" <<YML
- name: the uid boundary
  hosts: localhost
  connection: local
  gather_facts: false
  tasks:
    - name: report the uid the play runs under
      ansible.builtin.command: id
      changed_when: false
    - name: write an artifact the worker must be able to read back
      ansible.builtin.copy:
        content: "written by the play\n"
        dest: $work/artifact.txt
        mode: "0660"
    - name: read the worker's own key
      ansible.builtin.slurp:
        src: $key
YML
chmod 0660 "$work/project/play.yml"

set +e
/usr/local/bin/pulsix-setuid-exec /usr/local/bin/ansible-playbook \
	-i localhost, "$work/project/play.yml" > "$work/play.log" 2>&1
rc=$?
set -e
cat "$work/play.log"

if [ "$rc" -eq 0 ]; then
	echo "FAIL: the play succeeded; it must not be able to read $key"
	exit 1
fi
if ! grep -qi 'permission denied' "$work/play.log"; then
	echo "FAIL: the play failed for some other reason than permission denied"
	exit 1
fi
echo "PULSIX-UID-TEST: the play cannot read the worker key"

# The other direction. This shell is the worker uid: if it cannot read an
# artifact the play wrote, the run's event stream would be dead.
if ! grep -q 'written by the play' "$work/artifact.txt"; then
	echo "FAIL: the worker cannot read back the play's artifact"
	exit 1
fi
owner=$(stat -c %u "$work/artifact.txt")
if [ "$owner" != "$play_uid" ]; then
	echo "FAIL: the artifact is owned by uid $owner, want $play_uid — the play did not run under its own uid"
	exit 1
fi
echo "PULSIX-UID-TEST: the worker read back what the play wrote"
`
