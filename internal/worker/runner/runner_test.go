// Run orchestration against a fake ansible-runner binary: a shell script
// the test writes that dumps its environment and argv (so the tests assert
// exactly what the real invocation would get), emits the checked-in event
// fixture as its stdout JSONL and exits with a controllable code. The fake
// exercises the real path: same argv shape, same stdout handling, same
// cancellation semantics.
package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

// fakeRunnerEnv names the control variables the fake binary reads.
const (
	fakeEnvDump   = "FAKE_RUNNER_ENV"    // where the fake dumps its env
	fakeEnvArgs   = "FAKE_RUNNER_ARGS"   // where the fake dumps its argv
	fakeEnvEvents = "FAKE_RUNNER_EVENTS" // the JSONL fixture to emit
	fakeEnvExit   = "FAKE_RUNNER_EXIT"   // the process exit code
	fakeEnvSticky = "FAKE_RUNNER_STICKY" // set → ignore SIGTERM (cancel test)
	fakeEnvFlood  = "FAKE_RUNNER_FLOOD"  // set → flood ok-events in a loop, counting into the file (prompt-kill test)
)

// writeFakeRunner writes the fake ansible-runner binary: it dumps its env
// and its argv to the given files, optionally sticks (ignores SIGTERM — the
// cancel test) or floods (emits ok-events in a loop until killed — the
// prompt-kill test), then emits the event fixture and exits with the code.
func writeFakeRunner(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ansible-runner")
	script := "#!/bin/sh\n" +
		"env > \"$" + fakeEnvDump + "\"\n" +
		"printf '%s\\n' \"$@\" > \"$" + fakeEnvArgs + "\"\n" +
		"if [ -n \"$" + fakeEnvSticky + "\" ]; then\n" +
		"  trap '' TERM\n" +
		"  while :; do sleep 5; done\n" +
		"fi\n" +
		"if [ -n \"$" + fakeEnvFlood + "\" ]; then\n" +
		"  i=0\n" +
		"  while [ \"$i\" -lt 200 ]; do\n" +
		"    printf '%s\\n' '{\"event\": \"runner_on_ok\", \"event_data\": {\"host\": \"h\", \"task\": \"t\"}}'\n" +
		"    echo x >> \"$" + fakeEnvFlood + "\"\n" +
		"    sleep 0.01\n" +
		"    i=$((i+1))\n" +
		"  done\n" +
		"  exit 0\n" +
		"fi\n" +
		"cat \"$" + fakeEnvEvents + "\" 2>/dev/null\n" +
		"exit ${" + fakeEnvExit + ":-0}\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// fixtureAbs is the checked-in event fixture's absolute path.
func fixtureAbs(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("testdata/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// runJob builds a run job's claim the way internal/jobs ships it.
func runJob(t *testing.T) *workerpb.Job {
	t.Helper()
	return &workerpb.Job{
		Id:          "01900000-0000-7000-8000-000000000abc",
		Kind:        "run",
		OrgId:       orgID.String(),
		ZoneId:      "01900000-0000-7000-8000-000000000fff",
		Limit:       "web*",
		Playbook:    "- hosts: all\n  tasks:\n    - name: Install nginx\n      ansible.builtin.apt:\n        name: nginx\n",
		ExtraVars:   map[string]string{"run": `"smoke"`, "count": "2"},
		SshUsername: "ansible",
		InventoryDoc: `{"all": {"children": ["web"]}, "web": {"hosts": ["web1", "app1", "db1"]}, "_meta": {"hostvars": {
			"web1": {"ansible_host": "192.0.2.10", "ansible_user": "ansible", "ansible_ssh_common_args": "-o ProxyJump=deploy@192.0.2.9:2222"},
			"app1": {"ansible_host": "192.0.2.11", "ansible_user": "deploy"},
			"db1": {"ansible_host": "192.0.2.12", "ansible_user": "backup"}}}}`,
		Connections: map[string]string{
			"web1": `{"host_id":"` + webUUID + `","credential_id":"cred-1","bastions":["b1"],"bastion_credential_ids":["cred-b"],"bastion_source":"host"}`,
			"app1": `{"host_id":"` + appUUID + `","credential_id":"cred-2","bastion_source":"none"}`,
			"db1":  `{"host_id":"` + dbUUID + `","credential_id":"cred-3","bastion_source":"none"}`,
		},
		Credentials: map[string]string{
			"cred-1": secretJSON(t, credentials.Secret{PrivateKey: "host-key-material"}),
			"cred-2": secretJSON(t, credentials.Secret{PrivateKey: "app-key-material"}),
			"cred-3": secretJSON(t, credentials.Secret{PrivateKey: "db-key-material"}),
			"cred-b": secretJSON(t, credentials.Secret{PrivateKey: "bastion-key-material"}),
		},
	}
}

// runEnv builds the run environment over a fresh work dir.
func runEnv(t *testing.T) worker.RunEnv {
	t.Helper()
	return worker.RunEnv{
		WorkDir:      t.TempDir(),
		RepoCacheDir: filepath.Join(t.TempDir(), "repo-cache"),
		Config:       worker.Config{AnsibleRunnerBin: "ansible-runner", AnsibleBin: "ansible-playbook"},
	}
}

// collectSink records every forwarded event.
type collector struct {
	events []*workerpb.RunEvent
	err    error
}

func (c *collector) sink(ev *workerpb.RunEvent) error {
	if c.err != nil {
		return c.err
	}
	c.events = append(c.events, ev)
	return nil
}

func (c *collector) states() []string {
	out := make([]string, 0, len(c.events))
	for _, ev := range c.events {
		out = append(out, ev.GetState())
	}
	return out
}

// cleanFixtureAbs is the clean-recap fixture's absolute path: the same
// event shapes with a recap whose hosts all succeeded.
func cleanFixtureAbs(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("testdata/events-clean.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunForwardsEventsAndMapsStats(t *testing.T) {
	// The failure fixture's recap carries one failed and one unreachable
	// host: the run is failed, with the claim's stats contract, no matter
	// what the exit code says.
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	env := runEnv(t)
	c := &collector{}
	status, stats, err := (&Runner{RunnerBin: fake}).Run(
		context.Background(), job, env, c.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != worker.StatusFailed {
		t.Errorf("status = %q, want failed (the recap carries failures)", status)
	}

	// The event order: the runner's bookends and each fixture event once
	// (a mapped event carries its display text in its own payload; only
	// display-only lines become stdout events), and no seq — the loop owns
	// numbering.
	wantStates := []string{
		"run_start",
		"stdout",      // playbook_on_start banner
		"stdout",      // playbook_on_task_start banner
		"task",        // runner_on_start
		"ok",          // runner_on_ok
		"changed",     // runner_on_ok changed
		"failed",      // runner_on_failed
		"unreachable", // runner_on_unreachable
		"skipped",     // runner_on_skipped
		"stdout",      // the stats line's PLAY RECAP
		"run_end",
	}
	assertStates(t, c.states(), wantStates)
	for _, ev := range c.events {
		if ev.GetSeq() != 0 || ev.GetJobId() != "" || ev.GetAt() != nil {
			t.Errorf("the runner must not stamp identity (loop owns it): %+v", ev)
		}
	}

	// The stats map is the claim's contract: failed/unreachable host UUIDs
	// plus the recap's other host lists.
	assertStats(t, stats, map[string]any{
		"failed_hosts":      []string{appUUID},
		"unreachable_hosts": []string{dbUUID},
		"ok_hosts":          []string{webUUID},
		"changed_hosts":     []string{webUUID},
		"skipped_hosts":     []string{webUUID},
	})

	// The last event's payload is the run_end bookend's status.
	last := c.events[len(c.events)-1]
	if last.GetState() != "run_end" || !strings.Contains(last.GetPayload(), `"failed"`) {
		t.Errorf("run_end payload = %q", last.GetPayload())
	}
}

func TestRunSucceedsWithCleanRecap(t *testing.T) {
	// A recap with no failed or unreachable host and exit 0 is succeeded.
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, cleanFixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	status, stats, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), runEnv(t), func(*workerpb.RunEvent) error { return nil })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != worker.StatusSucceeded {
		t.Errorf("status = %q, want succeeded", status)
	}
	assertStats(t, stats, map[string]any{
		"failed_hosts":      []string{},
		"unreachable_hosts": []string{},
		"ok_hosts":          []string{appUUID, webUUID}, // host names sorted
		"changed_hosts":     []string{appUUID},
		"skipped_hosts":     []string{},
	})
}

func TestRunInventoryAndConfigFilesAreOwnerOnly(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	env := runEnv(t)
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	checkMode(t, filepath.Join(env.WorkDir, "inventory.json"), 0o600)
	checkMode(t, filepath.Join(env.WorkDir, "inventory"), 0o700)
	checkMode(t, filepath.Join(env.WorkDir, "ansible.cfg"), 0o600)
	checkMode(t, filepath.Join(env.WorkDir, "ssh_config"), 0o600)
	checkMode(t, filepath.Join(env.WorkDir, "keys", "cred-1"), 0o600)
	checkMode(t, filepath.Join(env.WorkDir, "keys", "cred-b"), 0o600)
	checkMode(t, filepath.Join(env.WorkDir, "env", "extravars"), 0o600)
}

func TestRunEnvIsHygienic(t *testing.T) {
	fake := writeFakeRunner(t)
	envDump := filepath.Join(t.TempDir(), "env")
	t.Setenv(fakeEnvDump, envDump)
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")
	// Decoys: the worker's own environment must not leak into the run.
	t.Setenv("PULSIX_ANSIBLE_RUNNER_BIN", "/usr/bin/decoy")
	t.Setenv("ANSIBLE_CONFIG", "/operator/leaked.cfg")
	t.Setenv("ANSIBLE_SSH_ARGS", "-o leaked=1")

	env := runEnv(t)
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	envText := string(raw)

	// Exactly the fixed ANSIBLE_* set, none of the operator's decoys.
	for _, want := range []string{
		"ANSIBLE_HOST_KEY_CHECKING=True",
		"ANSIBLE_RETRY_FILES_ENABLED=False",
		"ANSIBLE_SSH_ARGS=-F " + filepath.Join(env.WorkDir, "ssh_config"),
		"ANSIBLE_CONFIG=" + filepath.Join(env.WorkDir, "ansible.cfg"),
		"ANSIBLE_LOCAL_TEMP=" + filepath.Join(env.WorkDir, "ansible-local"),
		"HOME=" + filepath.Join(env.WorkDir, "home"),
		"PATH=",
	} {
		if !strings.Contains(envText, want) {
			t.Errorf("the run env lacks %q", want)
		}
	}
	for _, forbidden := range []string{"PULSIX_", "leaked.cfg", "leaked=1"} {
		if strings.Contains(envText, forbidden) {
			t.Errorf("the run env leaks %q", forbidden)
		}
	}

	// Credential material reaches the run only as 0600 file paths — never
	// as environment values.
	for _, secret := range []string{"host-key-material", "app-key-material", "db-key-material", "bastion-key-material"} {
		if strings.Contains(envText, secret) {
			t.Errorf("the run env leaks credential material %q", secret)
		}
	}
}

func TestRunArgvShapeIsPinned(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, filepath.Join(t.TempDir(), "args"))
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	env := runEnv(t)
	env.Config.AnsibleBin = "/opt/ansible/bin/ansible-playbook"
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The invocation is the pinned contract (the Dockerfile pins a
	// compatible ansible-runner version against it). A configured
	// ansible-playbook is NOT passed as -b/--binary: ansible-runner 2.4.3
	// then runs the binary without the playbook argument (found by the
	// real-ansible E2E); its directory leads the run's PATH instead.
	assertArgs(t, string(mustRead(t, os.Getenv(fakeEnvArgs))),
		"run", env.WorkDir, "--json",
		"--playbook", filepath.Join(env.WorkDir, "project", "playbook.yml"),
		"--inventory", filepath.Join(env.WorkDir, "inventory"),
		"--ident", "01900000",
		"--limit", "web*",
	)
	if envText := string(mustRead(t, os.Getenv(fakeEnvDump))); !strings.Contains(envText, "PATH=/opt/ansible/bin:") {
		t.Errorf("the configured ansible-playbook's directory does not lead PATH:\n%s", envText)
	}
}

// TestRunFallsBackToConfiguredRunnerBin pins the PULSIX_ANSIBLE_RUNNER_BIN
// fallback: a runner with no binary set uses the claim's configuration.
func TestRunFallsBackToConfiguredRunnerBin(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, filepath.Join(t.TempDir(), "args"))
	t.Setenv(fakeEnvEvents, cleanFixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	env := runEnv(t)
	env.Config.AnsibleRunnerBin = fake
	env.Config.AnsibleBin = "" // no -b flag: PATH discovery
	if _, _, err := (&Runner{}).Run(context.Background(), runJob(t), env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertArgs(t, string(mustRead(t, os.Getenv(fakeEnvArgs))),
		"run", env.WorkDir, "--json",
		"--playbook", filepath.Join(env.WorkDir, "project", "playbook.yml"),
		"--inventory", filepath.Join(env.WorkDir, "inventory"),
		"--ident", "01900000",
		"--limit", "web*",
	)
}

func TestRunExitCodeMapping(t *testing.T) {
	cases := []struct {
		name    string
		fixture func(*testing.T) string
		rc      string
		status  worker.Status
	}{
		{"exit 0 with a clean recap", cleanFixtureAbs, "0", worker.StatusSucceeded},
		{"exit 0 with failed hosts in the recap", fixtureAbs, "0", worker.StatusFailed},
		{"exit 2 (hosts failed)", fixtureAbs, "2", worker.StatusFailed},
		{"exit 3 with a clean recap", cleanFixtureAbs, "3", worker.StatusFailed},
		{"exit 1 (ansible error)", cleanFixtureAbs, "1", worker.StatusError},
		{"exit 5 (no hosts matched)", cleanFixtureAbs, "5", worker.StatusError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := writeFakeRunner(t)
			t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
			t.Setenv(fakeEnvEvents, c.fixture(t))
			t.Setenv(fakeEnvExit, c.rc)
			status, stats, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), runEnv(t), func(*workerpb.RunEvent) error { return nil })
			// An errored run says why (the exit code), so the job's error
			// is never empty; every other outcome has no error.
			if c.status == worker.StatusError {
				if err == nil || !strings.Contains(err.Error(), "exited with code "+c.rc) {
					t.Fatalf("Run error = %v, want the exit code named", err)
				}
			} else if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if status != c.status {
				t.Errorf("status = %q, want %q", status, c.status)
			}
			if c.status == worker.StatusFailed && stats == nil {
				t.Fatal("a failed run reports its stats")
			}
		})
	}
}

func TestRunWithoutStatsIsAnError(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, "") // nothing to emit
	t.Setenv(fakeEnvExit, "0")

	status, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), runEnv(t), func(*workerpb.RunEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "no run stats") {
		t.Fatalf("Run error = %v, want the missing stats named", err)
	}
	if status != worker.StatusError {
		t.Errorf("status = %q, want error (a stats-less success would lie)", status)
	}
}

func TestRunUnknownKindIsAnError(t *testing.T) {
	job := runJob(t)
	job.Kind = "connection_test"
	status, stats, err := (&Runner{RunnerBin: writeFakeRunner(t)}).Run(context.Background(), job, runEnv(t), func(*workerpb.RunEvent) error { return nil })
	if err == nil {
		t.Fatal("a kind the runner does not execute must fail")
	}
	if status != worker.StatusError || stats != nil {
		t.Errorf("status = %q stats = %v", status, stats)
	}
	if !strings.Contains(err.Error(), "connection_test") {
		t.Errorf("error names the kind: %v", err)
	}
}

func TestRunAdhocBuildsTheOneTaskPlaybook(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, filepath.Join(t.TempDir(), "args"))
	t.Setenv(fakeEnvEvents, cleanFixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	job.Kind = "adhoc"
	job.Playbook = ""
	job.Limit = ""
	job.KindPayload = `{"module":"shell","argv":"echo hello"}`
	env := runEnv(t)
	c := &collector{}
	status, stats, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, env, c.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != worker.StatusSucceeded {
		t.Errorf("status = %q", status)
	}

	// The playbook is the plan's Task 15 shape: shell module, the argv as
	// the command, executable bash, changed_when true.
	text := string(mustRead(t, filepath.Join(env.WorkDir, "project", "playbook.yml")))
	for _, want := range []string{"hosts: all", "gather_facts: false", "ansible.builtin.shell:", "executable: /bin/bash", "changed_when: true", "echo hello"} {
		if !strings.Contains(text, want) {
			t.Errorf("adhoc playbook lacks %q:\n%s", want, text)
		}
	}
	// No limit flag for ad-hoc jobs.
	assertArgs(t, string(mustRead(t, os.Getenv(fakeEnvArgs))),
		"run", env.WorkDir, "--json",
		"--playbook", filepath.Join(env.WorkDir, "project", "playbook.yml"),
		"--inventory", filepath.Join(env.WorkDir, "inventory"),
		"--ident", "01900000",
	)

	// The run view still gets its bookends and the fixture's events.
	if c.states()[0] != "run_start" || c.states()[len(c.states())-1] != "run_end" {
		t.Errorf("adhoc run bookends = %v", c.states())
	}
	if stats == nil {
		t.Error("the adhoc run reports its stats")
	}
}

func TestRunAdhocPayloadValidation(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{"empty payload", "", "no module and argv"},
		{"not json", `{oops`, "kind payload"},
		{"module not supported", `{"module":"raw","argv":"x"}`, "shell"},
		{"empty argv", `{"module":"shell","argv":"  "}`, "argv is empty"},
		{"oversized argv", `{"module":"shell","argv":"` + strings.Repeat("x", 9<<10) + `"}`, "exceeds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			job := runJob(t)
			job.Kind = "adhoc"
			job.Playbook = ""
			job.KindPayload = c.payload
			status, _, err := (&Runner{RunnerBin: writeFakeRunner(t)}).Run(context.Background(), job, runEnv(t), func(*workerpb.RunEvent) error { return nil })
			if err == nil {
				t.Fatalf("Run must fail, status = %q", status)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %v, want it to name %q", err, c.wantErr)
			}
		})
	}
}

func TestRunRepoPlaybookResolvesThroughTheCache(t *testing.T) {
	source := gitFixture(t)
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, filepath.Join(t.TempDir(), "args"))
	t.Setenv(fakeEnvEvents, cleanFixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	repoID := uuid.Must(uuid.NewV7())
	job.Playbook = "repo:" + repoID.String() + ":playbooks/site.yml"
	job.KindPayload = `{"repo":{"id":"` + repoID.String() + `","url":"` + source + `","branch":"main","subpath":"playbooks/site.yml"}}`
	env := runEnv(t)
	status, _, err := (&Runner{RunnerBin: fake, GitBin: gitOnPATH(t)}).Run(context.Background(), job, env, func(*workerpb.RunEvent) error { return nil })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != worker.StatusSucceeded {
		t.Errorf("status = %q", status)
	}

	// The playbook argument points into the org-scoped clone.
	clone := filepath.Join(env.RepoCacheDir, orgID.String(), repoID.String())
	assertArgs(t, string(mustRead(t, os.Getenv(fakeEnvArgs))),
		"run", env.WorkDir, "--json",
		"--playbook", filepath.Join(clone, "playbooks", "site.yml"),
		"--inventory", filepath.Join(env.WorkDir, "inventory"),
		"--ident", "01900000",
		"--limit", "web*",
	)

	// The repo fetch's git key is wiped when the fetch ends.
	if files, _ := os.ReadDir(filepath.Join(env.WorkDir, "gitkeys")); len(files) != 0 {
		t.Errorf("git key files survived: %v", files)
	}
}

func TestRunRepoPlaybookWithoutPayloadFails(t *testing.T) {
	job := runJob(t)
	repoID := uuid.Must(uuid.NewV7())
	job.Playbook = "repo:" + repoID.String() + ":playbooks/site.yml"
	job.KindPayload = `{}`
	status, _, err := (&Runner{RunnerBin: writeFakeRunner(t)}).Run(context.Background(), job, runEnv(t), func(*workerpb.RunEvent) error { return nil })
	if err == nil {
		t.Fatalf("Run must fail, status = %q", status)
	}
	if !strings.Contains(err.Error(), "repo payload") {
		t.Errorf("error = %v", err)
	}
}

func TestRunBadPlaybookRefFails(t *testing.T) {
	cases := []struct {
		name     string
		playbook string
	}{
		{"repo ref without subpath", "repo:" + uuid.Must(uuid.NewV7()).String() + ":"},
		{"repo ref without id", "repo::site.yml"},
		{"repo ref with junk id", "repo:not-a-uuid:site.yml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			job := runJob(t)
			job.Playbook = c.playbook
			job.KindPayload = `{"repo":{"id":"x"}}`
			if _, _, err := (&Runner{RunnerBin: writeFakeRunner(t)}).Run(context.Background(), job, runEnv(t), func(*workerpb.RunEvent) error { return nil }); err == nil {
				t.Error("a malformed repo reference must fail")
			}
		})
	}
}

func TestRunInlinePlaybookIsWrittenVerbatim(t *testing.T) {
	fake := writeFakeRunner(t)
	envDump := filepath.Join(t.TempDir(), "env")
	t.Setenv(fakeEnvDump, envDump)
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	playbook := "- hosts: all\n  tasks:\n    - name: custom\n      ansible.builtin.debug:\n"
	job.Playbook = playbook
	env := runEnv(t)
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(env.WorkDir, "project", "playbook.yml"))); got != playbook {
		t.Errorf("inline playbook =\n%q\nwant\n%q", got, playbook)
	}
}

func TestRunCancelSendsTermThenKill(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, "")
	t.Setenv(fakeEnvExit, "0")
	t.Setenv(fakeEnvSticky, "1") // ignores SIGTERM

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var status worker.Status
	var runErr error
	go func() {
		defer close(done)
		status, _, runErr = (&Runner{RunnerBin: fake, KillGrace: 150 * time.Millisecond}).Run(
			ctx, runJob(t), runEnv(t), func(*workerpb.RunEvent) error { return nil })
	}()
	time.Sleep(300 * time.Millisecond) // let the run start and stick
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end after cancellation (SIGKILL grace)")
	}
	if status != worker.StatusCancelled || runErr == nil {
		t.Errorf("status = %q err = %v, want cancelled + context.Canceled", status, runErr)
	}
}

func TestRunSinkErrorStopsTheRun(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	c := &collector{err: context.Canceled}
	_, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), runEnv(t), c.sink)
	if err == nil {
		t.Fatal("a broken event stream must fail the run")
	}
}

func TestRunSinkErrorKillsTheRunnerPromptly(t *testing.T) {
	// A runner that floods events until killed, a sink that errors on the
	// first one: the run must stop the subprocess immediately — the error
	// surfaces fast and the unobserved tail of the run is never emitted.
	const total = 200
	fake := writeFakeRunner(t)
	countFile := filepath.Join(t.TempDir(), "count")
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, filepath.Join(t.TempDir(), "args"))
	t.Setenv(fakeEnvEvents, "")
	t.Setenv(fakeEnvExit, "0")
	t.Setenv(fakeEnvFlood, countFile)

	env := runEnv(t)
	c := &collector{err: context.Canceled} // errors on the very first event
	start := time.Now()
	_, _, err := (&Runner{RunnerBin: fake, KillGrace: 250 * time.Millisecond}).Run(
		context.Background(), runJob(t), env, c.sink)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a broken stream must fail the run")
	}
	if elapsed > 2*time.Second {
		t.Errorf("the run took %v to fail; the runner must die promptly, not finish unobserved", elapsed)
	}
	// The unimpeded fake would have emitted all 200 events (10 ms apart,
	// ~2 s); a prompt kill leaves only a fraction on the counter.
	if n := countLines(t, countFile); n > total/2 {
		t.Errorf("the runner kept emitting after the stream broke: %d of %d events", n, total)
	}
}

func TestRunBrokenInventoryFailsClosed(t *testing.T) {
	job := runJob(t)
	job.InventoryDoc = `{oops`
	status, _, err := (&Runner{RunnerBin: writeFakeRunner(t)}).Run(context.Background(), job, runEnv(t), func(*workerpb.RunEvent) error { return nil })
	if err == nil || status != worker.StatusError {
		t.Errorf("status = %q err = %v, want error", status, err)
	}
	if !strings.Contains(err.Error(), "inventory document") {
		t.Errorf("error names the cause: %v", err)
	}
}

func TestRunBrokenExtraVarFailsClosed(t *testing.T) {
	job := runJob(t)
	job.ExtraVars = map[string]string{"oops": "not json"}
	if _, _, err := (&Runner{RunnerBin: writeFakeRunner(t)}).Run(context.Background(), job, runEnv(t), func(*workerpb.RunEvent) error { return nil }); err == nil {
		t.Error("a non-JSON extra var must fail")
	}
}

func TestRunEmptyInventoryRunsAgainstNothing(t *testing.T) {
	fake := writeFakeRunner(t)
	envDump := filepath.Join(t.TempDir(), "env")
	t.Setenv(fakeEnvDump, envDump)
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	job.InventoryDoc = "{}"
	env := runEnv(t)
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The document ships as-is; the run against an empty inventory is an
	// ansible-level condition (exit 5), not a materialization error.
	if got := string(mustRead(t, filepath.Join(env.WorkDir, "inventory.json"))); got != "{}" {
		t.Errorf("inventory.json = %q", got)
	}
}

// TestRunnerSpawnsUnderThePlayUID pins the built *exec.Cmd directly (per the
// plan: the uid boundary itself is proven by the Task 4 integration test
// against the real image, and a unit test cannot setuid). With PlayUID set
// the command must be the setuid helper, carrying the uid and then the real
// runner binary ahead of the runner's own arguments. The drop happens inside
// the helper and never through SysProcAttr.Credential: Credential needs
// CAP_SETUID on the worker, and a process holding that can setuid(0), so a
// Credential reappearing here is a regression into the design's anti-goal.
// With PlayUID 0 the invocation must be byte-for-byte what it always was.
// Setpgid stays true either way — the cancel path signals the process group
// and breaks without it.
func TestRunnerSpawnsUnderThePlayUID(t *testing.T) {
	const (
		helper = "/opt/pulsix/setuid-exec"
		// Absolute on purpose: exec.Command only consults PATH for a name
		// without a separator, which would make cmd.Path depend on whether
		// ansible-runner happens to be installed on the machine.
		bin = "/opt/ansible/ansible-runner"
	)
	args := []string{"run", "--json"}

	cases := map[string]struct {
		playUID  uint32
		helper   string
		wantPath string
		wantArgs []string
	}{
		"play uid set": {
			playUID:  12345,
			helper:   helper,
			wantPath: helper,
			wantArgs: []string{helper, "12345", bin, "run", "--json"},
		},
		"play uid set, helper left at its default": {
			playUID:  12345,
			wantPath: defaultSetuidHelper,
			wantArgs: []string{defaultSetuidHelper, "12345", bin, "run", "--json"},
		},
		"play uid unset": {
			helper:   helper,
			wantPath: bin,
			wantArgs: []string{bin, "run", "--json"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := runEnv(t)
			env.Config.PlayUID = tc.playUID
			env.Config.SetuidHelper = tc.helper
			cmd := newRunnerCmd(bin, args, env, nil)

			if !cmd.SysProcAttr.Setpgid {
				t.Error("Setpgid = false, want true regardless of PlayUID")
			}
			if cmd.SysProcAttr.Credential != nil {
				t.Errorf("Credential = %+v, want nil always: the drop belongs to the setuid helper, because Credential would need CAP_SETUID on the worker", cmd.SysProcAttr.Credential)
			}
			if cmd.Path != tc.wantPath {
				t.Errorf("cmd.Path = %q, want %q", cmd.Path, tc.wantPath)
			}
			if !slices.Equal(cmd.Args, tc.wantArgs) {
				t.Errorf("cmd.Args = %q, want %q", cmd.Args, tc.wantArgs)
			}
		})
	}
}

// TestRunnerRefusedSpawnNamesTheVariable: with PlayUID set, the spawn goes
// through the setuid helper, so the way it fails is a helper that is not
// there (a worker outside the Pulsix image, a bad
// PULSIX_WORKER_SETUID_HELPER). The error must name the helper path, the
// uid and PULSIX_WORKER_PLAY_UID — a bare "no such file or directory"
// sends an operator nowhere useful. There is no fallback: the job fails
// rather than quietly running the play as the worker itself.
func TestRunnerRefusedSpawnNamesTheVariable(t *testing.T) {
	const playUID = uint32(12345)
	absent := filepath.Join(t.TempDir(), "no-such-setuid-helper")

	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	env := runEnv(t)
	env.Config.PlayUID = playUID
	env.Config.SetuidHelper = absent
	_, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, env, func(*workerpb.RunEvent) error { return nil })
	if err == nil {
		t.Fatal("Run must fail: the setuid helper does not exist")
	}
	for _, want := range []string{"PULSIX_WORKER_PLAY_UID", "PULSIX_WORKER_SETUID_HELPER", absent, fmt.Sprint(playUID)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
}

// TestRunCancelThroughTheSetuidHelperStillKillsTheGroup is the one claim
// the argv assertions above cannot make: Setpgid is set on the HELPER, and
// the cancel path signals that process group. It only reaches the runner
// because the helper execs rather than forks — same pid, same group. A
// stand-in helper (it checks argv[1] and execs argv[2:], like the real one,
// but cannot setuid as an unprivileged test process) is enough to prove the
// chain; the privileged half is the image test's job.
func TestRunCancelThroughTheSetuidHelperStillKillsTheGroup(t *testing.T) {
	const playUID = uint32(12345)
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, "")
	t.Setenv(fakeEnvExit, "0")
	t.Setenv(fakeEnvSticky, "1") // ignores SIGTERM, so only the group SIGKILL ends it

	env := runEnv(t)
	env.Config.PlayUID = playUID
	env.Config.SetuidHelper = writeStandInSetuidHelper(t, playUID)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var status worker.Status
	var runErr error
	go func() {
		defer close(done)
		status, _, runErr = (&Runner{RunnerBin: fake, KillGrace: 150 * time.Millisecond}).Run(
			ctx, runJob(t), env, func(*workerpb.RunEvent) error { return nil })
	}()
	time.Sleep(300 * time.Millisecond) // let the helper exec the runner and stick
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end after cancellation: the group signal did not reach the runner through the helper")
	}
	if status != worker.StatusCancelled || runErr == nil {
		t.Errorf("status = %q err = %v, want cancelled + context.Canceled", status, runErr)
	}
}

// TestSetuidHelperRefusalReachesTheOperator pins both halves of the one
// path the helper's own wording travels. Only a MISSING helper fails at
// cmd.Start(); every other refusal it can make — a uid that disagrees with
// the one it was built for, a lost setuid bit, a drop the kernel did not
// make — starts fine and exits 1, and the generic branch would render that
// as "ansible-runner exited with code 1", throwing the reason away. The
// other half matters just as much: an ansible-runner that genuinely exits 1
// must still read exactly as it does today.
func TestSetuidHelperRefusalReachesTheOperator(t *testing.T) {
	const playUID = uint32(12345)
	const refusal = "pulsix-setuid-exec: asked for uid 12345 but this helper is built for uid 65533"

	cases := map[string]struct {
		playUID uint32
		stderr  string
		want    string
		wantNot string
	}{
		"the helper refuses": {
			playUID: playUID,
			stderr:  refusal + "\n",
			want:    refusal,
		},
		"the helper names the variable even when its own line does not": {
			playUID: playUID,
			stderr:  "pulsix-setuid-exec: setgid: Operation not permitted\n",
			want:    "PULSIX_WORKER_PLAY_UID=12345",
		},
		"ansible-runner itself exits 1": {
			playUID: playUID,
			stderr:  "ERROR! the playbook could not be found\n",
			want:    "ansible-runner exited with code 1",
			wantNot: "pulsix-setuid-exec",
		},
		"the feature is off": {
			// Nothing can have come from a helper that was never invoked;
			// a runner echoing the prefix must not be mistaken for one.
			playUID: 0,
			stderr:  refusal + "\n",
			want:    "ansible-runner exited with code 1",
			wantNot: "asked for uid",
		},
		"the extra-vars guard still wins its own case": {
			playUID: playUID,
			stderr:  extraVarsGuardVar,
			wantNot: "ansible-runner exited with code 1",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			status, _, errText := summarize(1, nil, nil, tc.stderr, nil, tc.playUID)
			if status != worker.StatusError {
				t.Errorf("status = %q, want %q", status, worker.StatusError)
			}
			if tc.want != "" && !strings.Contains(errText, tc.want) {
				t.Errorf("errText = %q, want it to contain %q", errText, tc.want)
			}
			if tc.wantNot != "" && strings.Contains(errText, tc.wantNot) {
				t.Errorf("errText = %q, want it NOT to contain %q", errText, tc.wantNot)
			}
		})
	}
}

// TestRunSurfacesASetuidHelperRefusal is the same property end to end: a
// helper that refuses and exits 1 (what the real one does for every failure
// but its own absence) must leave its words on the job's error, not in a
// pipe nobody reads.
func TestRunSurfacesASetuidHelperRefusal(t *testing.T) {
	const playUID = uint32(12345)
	refusing := filepath.Join(t.TempDir(), "refusing-setuid-exec")
	script := "#!/bin/sh\n" +
		"echo \"pulsix-setuid-exec: asked for uid $1 but this helper is built for uid 65533\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(refusing, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, "")
	t.Setenv(fakeEnvExit, "0")

	env := runEnv(t)
	env.Config.PlayUID = playUID
	env.Config.SetuidHelper = refusing

	status, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), runJob(t), env, func(*workerpb.RunEvent) error { return nil })
	if status != worker.StatusError || err == nil {
		t.Fatalf("status = %q err = %v, want an error status with a reason", status, err)
	}
	for _, want := range []string{"asked for uid 12345", "PULSIX_WORKER_PLAY_UID=12345"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "exited with code 1") {
		t.Errorf("error = %v, want the helper's reason instead of the generic exit-code text", err)
	}
}

// writeStandInSetuidHelper writes an unprivileged stand-in for
// deploy/worker/setuid-exec.c: the same contract (refuse a uid that is not
// the one it was built for, then exec argv[2:]) without the uid drop, which
// a test process may not do.
func writeStandInSetuidHelper(t *testing.T, uid uint32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stand-in-setuid-exec")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" != \"" + strconv.FormatUint(uint64(uid), 10) + "\" ]; then\n" +
		"  echo \"stand-in helper: asked for uid $1\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"shift\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// Claim uuids the stats mapping asserts on: the ansible host names web1,
// app1 and db1 map through the claim's connections.
var (
	webUUID = uuid.Must(uuid.NewV7()).String()
	appUUID = uuid.Must(uuid.NewV7()).String()
	dbUUID  = uuid.Must(uuid.NewV7()).String()
)

// mustRead reads a file, failing the test.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// checkMode asserts a run file's permission bits.
func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

// countLines counts a flood counter file's non-empty lines; a missing file
// counts as zero (the fake may be killed before its first append).
func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	n := 0
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// assertStates compares the forwarded event states against the want list.
func assertStates(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("state %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// assertStats compares the run's stats map against the claim contract.
func assertStats(t *testing.T, stats map[string]any, want map[string]any) {
	t.Helper()
	if stats == nil {
		t.Fatal("no stats")
	}
	for key, wantList := range want {
		got, ok := stats[key].([]string)
		if !ok {
			t.Errorf("stats[%q] = %#v, want a string list", key, stats[key])
			continue
		}
		want := wantList.([]string)
		if len(got) != len(want) {
			t.Errorf("stats[%q] = %v, want %v", key, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("stats[%q][%d] = %q, want %q", key, i, got[i], want[i])
			}
		}
	}
	if len(stats) != len(want) {
		t.Errorf("stats carries %d keys (%v), want %d", len(stats), stats, len(want))
	}
}

// assertArgs asserts the fake's recorded argv (one argument per line):
// exactly the want list, in order.
func assertArgs(t *testing.T, dump string, want ...string) {
	t.Helper()
	var got []string
	for _, l := range strings.Split(dump, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			got = append(got, l)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("args[%d] = %q, want %q", i, got[i], w)
		}
	}
}
