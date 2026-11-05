// The executors' full delegation into the real ansible-runner machinery,
// driven through the fake runner binary and a local git fixture. This file
// lives in the external test package so it can reach the runner executor —
// the delegation seam the internal tests cover only through a capture
// fake.
package worker_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/playbooks"
	"github.com/noixdev/pulsix/internal/worker/runner"
)

// envDump/argsPath/eventsPath name the fake runner's control variables
// (distinct from the runner package's own fake's names).
const (
	e2eDump   = "FAKE_DUMP_PATH"
	e2eArgs   = "FAKE_ARGS_PATH"
	e2eEvents = "FAKE_EVENTS_PATH"
	e2eExit   = "FAKE_EXIT_CODE"
)

// writeFakeRunner builds the fake ansible-runner binary.
func writeFakeRunner(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/ansible-runner"
	script := "#!/bin/sh\n" +
		"env > \"$" + e2eDump + "\"\n" +
		"printf '%s\\n' \"$@\" > \"$" + e2eArgs + "\"\n" +
		"cat \"$" + e2eEvents + "\" 2>/dev/null\n" +
		"exit ${" + e2eExit + ":-0}\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := t.TempDir() + "/events.jsonl"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

const cleanRecap = `{"event": "playbook_on_stats", "event_data": {"changed": {"web1": 1}, "dark": {}, "failures": {}, "ok": {"web1": 2}, "skipped": {}}}`

// orgID is the claimed job's organization id.
var orgID = uuid.Must(uuid.NewV7())

func TestBootstrapExecutorDelegatesToTheRunner(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(e2eDump, t.TempDir()+"/env")
	t.Setenv(e2eArgs, t.TempDir()+"/args")
	t.Setenv(e2eEvents, writeFixture(t, cleanRecap))
	t.Setenv(e2eExit, "0")

	secretJSON := `{"password":"one-time-pw"}`
	payload, err := json.Marshal(map[string]any{
		"host":       map[string]any{"id": "host-uuid", "name": "web1", "address": "192.0.2.10", "port": 22},
		"login_user": "root", "login": secretJSON, "public_key": "ssh-ed25519 AAAA pubkey", "pulsix_user": "ansible",
	})
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	env := worker.RunEnv{WorkDir: workDir, RepoCacheDir: t.TempDir(),
		Config: worker.Config{AnsibleRunnerBin: fake, AnsibleBin: "ansible-playbook"}}
	c := &eventCollector{}
	exec := worker.NewBootstrapExecutor(&runner.Runner{RunnerBin: fake})
	status, stats, err := exec.Run(context.Background(), &workerpb.Job{
		Id: "01900000-0000-7000-8000-000000000abc", Kind: "bootstrap",
		OrgId: orgID.String(), KindPayload: string(payload),
	}, env, c.sink)
	if err != nil || status != worker.StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}

	// The invocation is the pinned runner form, the playbook is the
	// embedded bootstrap YAML verbatim, and the one-time login rode in as
	// ansible_password (the runner's merge folded it in).
	assertArgs(t, mustRead(t, os.Getenv(e2eArgs)),
		"run", workDir, "--json",
		"--playbook", filepath.Join(workDir, "project", "playbook.yml"),
		"--inventory", filepath.Join(workDir, "inventory"),
		"--ident", "01900000",
	)
	if got := mustRead(t, filepath.Join(workDir, "project", "playbook.yml")); got != string(mustReadEmbedded(t, playbooks.NameBootstrap)) {
		t.Error("the bootstrap did not materialize the embedded playbook verbatim")
	}
	inv := mustRead(t, filepath.Join(workDir, "inventory.json"))
	for _, want := range []string{`"ansible_host":"192.0.2.10"`, `"ansible_user":"root"`, `"ansible_password":"one-time-pw"`} {
		if !strings.Contains(inv, want) {
			t.Errorf("merged inventory lacks %q:\n%s", want, inv)
		}
	}
	// The one-time password never enters the runner's environment.
	if strings.Contains(mustRead(t, os.Getenv(e2eDump)), "one-time-pw") {
		t.Error("the run env leaks the one-time password")
	}
	if stats == nil {
		t.Error("the bootstrap reports its stats")
	}
	// The bookends are truthful about what ran.
	first, last := c.events[0], c.events[len(c.events)-1]
	if first.GetState() != "run_start" || !strings.Contains(first.GetPayload(), `"bootstrap"`) {
		t.Errorf("run_start = %+v", first)
	}
	if last.GetState() != "run_end" {
		t.Errorf("run_end = %+v", last)
	}
}

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
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func mustReadEmbedded(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := playbooks.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type eventCollector struct {
	events []*workerpb.RunEvent
}

func (c *eventCollector) sink(ev *workerpb.RunEvent) error {
	c.events = append(c.events, ev)
	return nil
}

func TestRotateExecutorFailedRecapIsFailed(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(e2eDump, t.TempDir()+"/env")
	t.Setenv(e2eEvents, writeFixture(t, failedRecapConst))
	t.Setenv(e2eExit, "0")

	payload, err := json.Marshal(map[string]any{
		"host":            map[string]any{"id": "host-uuid", "name": "web1", "address": "192.0.2.10", "port": 22},
		"username":        "ansible",
		"old_credential":  `{"password":"old-pw"}`,
		"new_public_key":  "ssh-ed25519 AAAA newpub",
		"new_private_key": `{"private_key":"new-key"}`,
		"pulsix_user":     "ansible",
	})
	if err != nil {
		t.Fatal(err)
	}
	exec := worker.NewRotateExecutor(&runner.Runner{RunnerBin: fake})
	status, stats, err := exec.Run(context.Background(), &workerpb.Job{
		Id: "01900000-0000-7000-8000-000000000abc", Kind: "rotate_key",
		OrgId: orgID.String(), KindPayload: string(payload),
	}, worker.RunEnv{WorkDir: t.TempDir()}, func(*workerpb.RunEvent) error { return nil })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != worker.StatusFailed {
		t.Fatalf("status = %q, want failed (a failed playbook is a legitimate outcome)", status)
	}
	if failed := stats["failed_hosts"].([]string); len(failed) != 1 || failed[0] != "host-uuid" {
		t.Fatalf("stats = %v", stats)
	}
}

const failedRecapConst = `{"event": "playbook_on_stats", "event_data": {"changed": {}, "dark": {}, "failures": {"web1": 1}, "ok": {"web1": 1}, "skipped": {}}}`

func TestSyncExecutorFetchesThroughTheRealRunner(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("no git binary for the fixture: %v", err)
	}
	source := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=pulsix", "GIT_AUTHOR_EMAIL=pulsix@example.org",
			"GIT_COMMITTER_NAME=pulsix", "GIT_COMMITTER_EMAIL=pulsix@example.org")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(source, "init", "-b", "main")
	if err := os.WriteFile(source+"/requirements.yml", []byte("# no galaxy deps for the fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(source, "add", ".")
	run(source, "commit", "-m", "roles")
	head, err := exec.Command(git, "-C", source, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}

	// The fake galaxy succeeds and stays observable: an empty requirements
	// file still exercises the install path.
	galaxy := t.TempDir() + "/ansible-galaxy"
	galaxyArgs := t.TempDir() + "/galaxy-args"
	if err := os.WriteFile(galaxy, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+galaxyArgs+"\"\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	runExec := runner.NewRunner("", "", git, 0, nil)
	repoID := uuid.Must(uuid.NewV7())
	payload, err := json.Marshal(map[string]any{
		"repo": map[string]any{"id": repoID.String(), "url": source, "branch": "main", "subpath": ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := worker.RunEnv{WorkDir: t.TempDir(), RepoCacheDir: t.TempDir()}
	events := &eventCollector{}
	exec := worker.NewSyncExecutor(runExec, galaxy)
	status, stats, err := exec.Run(context.Background(), &workerpb.Job{
		Id: "01900000-0000-7000-8000-000000000abc", Kind: "sync_repo",
		OrgId: orgID.String(), KindPayload: string(payload),
	}, env, events.sink)
	if err != nil || status != worker.StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	if stats["requirements_installed"] != true {
		t.Fatalf("stats = %v", stats)
	}
	// The variable sources name the commit the clone is at: the fixture's
	// HEAD.
	raw, _ := stats[worker.VarSourcesStatsKey].(json.RawMessage)
	var sources struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(raw, &sources); err != nil || sources.Commit != strings.TrimSpace(string(head)) {
		t.Fatalf("var_sources commit = %q (%v), want %q", sources.Commit, err, strings.TrimSpace(string(head)))
	}

	// The clone landed org-scoped, and the galaxy invocation points at the
	// clone's sibling path (the real ansible-galaxy creates -p; the fake
	// only records its argv).
	clone := filepath.Join(env.RepoCacheDir, orgID.String(), repoID.String())
	if _, err := os.Stat(filepath.Join(clone, ".git")); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if args := mustRead(t, galaxyArgs); !strings.Contains(args, clone+".galaxy") {
		t.Errorf("galaxy args = %q", args)
	}
	// The result event carries the repo id for the run view.
	if len(events.events) != 1 || events.events[0].GetState() != "ok" {
		t.Fatalf("events = %+v", events.events)
	}
	if !strings.Contains(events.events[0].GetPayload(), repoID.String()) {
		t.Errorf("result event = %q", events.events[0].GetPayload())
	}
}
