// Edge branches of the run's pure helpers and materialization: error paths
// the orchestration tests cannot reach without contriving file-system
// breakage. Direct, small, table-driven — the coverage floor (90%) is
// earned here, not by weakening the real-path tests.
package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/worker"
	"gopkg.in/yaml.v3"
)

func TestParseEventStatsErrorsAreAdvisory(t *testing.T) {
	cases := []struct {
		name string
		line string
		err  bool
	}{
		{"stats event_data not an object", `{"event": "playbook_on_stats", "event_data": 7}`, true},
		{"stats event_data not valid json", `{"event": "playbook_on_stats", "event_data": {oops}}`, true},
		{"counts are strings", `{"event": "playbook_on_stats", "event_data": {"ok": {"web1": "2"}}}`, false},
		{"counts are floats", `{"event": "playbook_on_stats", "event_data": {"ok": {"web1": 2.0}}}`, false},
		{"count junk is ignored", `{"event": "playbook_on_stats", "event_data": {"ok": {"web1": "nope"}}}`, false},
		{"recap not a map", `{"event": "playbook_on_stats", "event_data": {"ok": 3}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ok, err := ParseEvent([]byte(c.line))
			if (err != nil) != c.err {
				t.Errorf("err = %v, want error %t", err, c.err)
			}
			if c.err && ok {
				t.Error("an unreadable stats event produces no event")
			}
		})
	}
}

func TestParseEventStringCountsAndFloats(t *testing.T) {
	ev, ok, err := ParseEvent([]byte(`{"event": "playbook_on_stats", "event_data": {"ok": {"web1": "2"}, "dark": {"db1": 1.0}}}`))
	if err != nil || !ok {
		t.Fatalf("ok=%t err=%v", ok, err)
	}
	if s := ev.Stats["web1"]; s.Ok != 2 {
		t.Errorf("web1 ok = %d, want 2 (string counts parse)", s.Ok)
	}
	if s := ev.Stats["db1"]; s.Unreachable != 1 {
		t.Errorf("db1 dark = %d, want 1 (float counts parse)", s.Unreachable)
	}
}

func TestWriteTextFileFailsClosed(t *testing.T) {
	// The parent path is a regular file: creating the parent fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(filepath.Join(blocker, "child", "file.txt"), "x", 0o600); err == nil {
		t.Error("a parent that is a file must fail")
	}

	// The target path is a directory: the write fails.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeTextFile(filepath.Join(dir, "sub"), "x", 0o600); err == nil {
		t.Error("a directory target must fail")
	}
}

func TestWriteAskpassFailsClosed(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeAskpass(filepath.Join(blocker, "nope"), "r", "t"); err == nil {
		t.Error("an unwritable work dir must fail the askpass write")
	}
}

func TestRunnerDefaults(t *testing.T) {
	// NewRunner wires the fields; Run falls back per field when unset.
	r := NewRunner("", "", "", 0, nil)
	if r.RunnerBin != "" || r.GitBin != "" || r.AnsibleBin != "" {
		t.Fatalf("NewRunner = %+v", r)
	}
	if r.log() == nil {
		t.Error("the nil logger falls back to slog's default")
	}
	injected := discardLogger()
	withLog := &Runner{Log: injected}
	if withLog.log() != injected {
		t.Error("the injected logger wins")
	}
	if ident("short") != "short" {
		t.Errorf("ident = %q, want the short id verbatim", ident("short"))
	}
}

func TestSubprocessEnvStripsPrivateVars(t *testing.T) {
	env := stripPrivateEnv([]string{
		"PATH=/usr/bin", "ANSIBLE_CONFIG=/leaked.cfg", "ANSIBLE_HOST_KEY_CHECKING=True",
		"PULSIX_ANSIBLE_RUNNER_BIN=x", "PULSIX_MANAGER_ADDR=y",
	})
	if len(env) != 1 || env[0] != "PATH=/usr/bin" {
		t.Errorf("stripPrivateEnv = %v", env)
	}
}

func TestSummarizeSignalIsAnError(t *testing.T) {
	status, stats, errText := summarize(-1, nil, os.ErrDeadlineExceeded, "", nil, 0)
	if status != worker.StatusError || stats != nil || errText == "" {
		t.Errorf("signal run: status=%q stats=%v err=%q", status, stats, errText)
	}
}

func TestBuildStatsMapsIndependentLists(t *testing.T) {
	// A host that fails one task and loses connection on the next lands in
	// both lists; a host the claim does not name stays a bare name. The
	// lists order by ansible host name.
	stats := map[string]hostStat{
		"web1":  {Ok: 1, Failed: 1, Unreachable: 1},
		"ghost": {Unreachable: 1},
	}
	out := buildStats(stats, map[string]string{
		"web1": `{"host_id":"` + webUUID + `"}`,
	})
	if got := out[statsFailedHosts].([]string); len(got) != 1 || got[0] != webUUID {
		t.Errorf("failed_hosts = %v", got)
	}
	if got := out[statsUnreachableHosts].([]string); len(got) != 2 || got[0] != "ghost" || got[1] != webUUID {
		t.Errorf("unreachable_hosts = %v (bare names stay bare)", got)
	}
	if got := out[statsOKHosts].([]string); len(got) != 1 || got[0] != webUUID {
		t.Errorf("ok_hosts = %v", got)
	}
}

func TestTruncateKeepsTheTail(t *testing.T) {
	if got := truncate("short", 100); got != "short" {
		t.Errorf("truncate = %q", got)
	}
	long := strings.Repeat("x", 9000)
	if got := truncate(long, 4<<10); got != long[len(long)-4<<10:] {
		t.Errorf("truncate keeps the last bytes")
	}
}

func TestMergeInventoryWithoutCredentialIsInert(t *testing.T) {
	// A connection that names no credential at all: the hostvars keep the
	// rendered shape, no key file is invented.
	conns := map[string]string{"web1": `{"host_id":"` + testHostID + `","bastion_source":"none"}`}
	merged, hosts, keys, err := mergeInventory(testDoc(), conns, nil, "", "/w/keys", nil)
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	if varsOf(t, merged, "web1")["ansible_ssh_private_key_file"] != nil {
		t.Error("no credential, no key file var")
	}
	if len(hosts) != 0 || len(keys) != 0 {
		t.Errorf("hosts = %v keys = %v", hosts, keys)
	}
}

func TestParseProxyJumpEdgeShapes(t *testing.T) {
	cases := []struct {
		name string
		args string
		ok   bool
	}{
		{"empty chain value", "-o ProxyJump=   ", false},
		{"value with trailing args", "-o ProxyJump=u@h:2222 -o BatchMode=yes", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, chain, ok := parseProxyJump(c.args)
			if ok != c.ok {
				t.Errorf("ok = %t", ok)
			}
			if ok && chain != "u@h:2222" {
				t.Errorf("chain = %q, the option ends the value", chain)
			}
		})
	}
}

func TestRewriteProxyJumpSkipsUnparseableChains(t *testing.T) {
	if got, ok := rewriteProxyJump("-o ProxyJump=a,b", "", "x"); ok || got != "-o ProxyJump=a,b" {
		t.Errorf("an empty parsed chain must not rewrite, got %q %t", got, ok)
	}
}

func TestGitCredentialNeedsNoSecret(t *testing.T) {
	// A credential-less Secret JSON: no key, no token — the fetch runs
	// without any credential plumbing.
	var cred gitCredential
	if err := jsonUnmarshal(`{}`, &cred); err != nil {
		t.Fatalf("empty secret: %v", err)
	}
	if cred.PrivateKey != "" || cred.Token != "" {
		t.Fatalf("gitCredential = %+v", cred)
	}
}

func TestFetchRepoFailsClosedOnBrokenCredential(t *testing.T) {
	r := &Runner{GitBin: gitOnPATH(t)}
	_, err := r.fetchRepo(context.Background(), gitRepoSpec{
		OrgID: orgID, RepoID: repoID, URL: "https://example.org/x.git", Branch: "main", Subpath: "site.yml",
	}, `{oops`, t.TempDir(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "git credential") {
		t.Errorf("a broken secret JSON must fail: %v", err)
	}
}

func TestFetchRepoRequiresBranch(t *testing.T) {
	r := &Runner{GitBin: gitOnPATH(t)}
	if _, err := r.fetchRepo(context.Background(), gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: "https://example.org/x.git", Subpath: "site.yml"}, "", t.TempDir(), t.TempDir()); err == nil {
		t.Error("a branchless repo spec must fail")
	}
}

func TestFetchRepoGitBinMissing(t *testing.T) {
	// The configured git binary does not exist: the failure names the
	// command, not the URL.
	r := &Runner{GitBin: filepath.Join(t.TempDir(), "no-such-git")}
	source := gitFixture(t)
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/site.yml"}
	if _, err := r.fetchRepo(context.Background(), spec, "", t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("a missing git binary must fail")
	} else if !strings.Contains(err.Error(), "git clone") {
		t.Errorf("error names the command: %v", err)
	}
}

func TestFetchRepoStaleCacheDirIsRecloned(t *testing.T) {
	// A leftover that is not a clone (a killed earlier run): re-cloned,
	// not built on.
	source := gitFixture(t)
	cacheRoot, workDir := t.TempDir(), t.TempDir()
	target := filepath.Join(cacheRoot, orgID.String(), repoID.String())
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "junk"), []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Runner{GitBin: gitOnPATH(t)}
	playbook, err := r.fetchRepo(context.Background(), gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/site.yml"}, "", cacheRoot, workDir)
	if err != nil {
		t.Fatalf("fetchRepo: %v", err)
	}
	if _, err := os.Stat(playbook); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "junk")); err == nil {
		t.Error("the stale leftover must be gone after the re-clone")
	}
}

func TestFetchRepoCacheRootFailure(t *testing.T) {
	// The cache root is a regular file: MkdirAll fails before any git run.
	source := gitFixture(t)
	cacheRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(cacheRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Runner{GitBin: gitOnPATH(t)}
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: source, Branch: "main", Subpath: "playbooks/site.yml"}
	if _, err := r.fetchRepo(context.Background(), spec, "", cacheRoot, t.TempDir()); err == nil {
		t.Error("an unusable cache root must fail")
	}
}

func TestSafeSubpathRejectsAbsoluteAndBackslash(t *testing.T) {
	for _, sub := range []string{"/abs/path.yml", `dir\file.yml`, "a/../b.yml"} {
		if _, err := safeSubpath(sub); err == nil {
			t.Errorf("safeSubpath(%q) must fail", sub)
		}
	}
	if got, err := safeSubpath("playbooks/site.yml"); err != nil || got != filepath.Join("playbooks", "site.yml") {
		t.Errorf("safeSubpath = %q, %v", got, err)
	}
}

func TestRepoSpecFailsClosed(t *testing.T) {
	job := runJob(t)
	job.OrgId = "not-a-uuid"
	if _, _, err := repoSpec(job, uuid.Must(uuid.NewV7())); err == nil {
		t.Error("a malformed org id must fail")
	}
	job = runJob(t)
	job.KindPayload = `{oops`
	if _, _, err := repoSpec(job, uuid.Must(uuid.NewV7())); err == nil {
		t.Error("a broken kind payload must fail")
	}
	job = runJob(t)
	job.KindPayload = `{"repo":{"id":"mismatched"}}`
	if _, _, err := repoSpec(job, uuid.Must(uuid.NewV7())); err == nil {
		t.Error("a payload naming another repo must fail")
	}
}

func TestMaterializeInventoryFailsClosedOnWrite(t *testing.T) {
	// The work dir's inventory path is a directory: the inventory write
	// fails before anything else.
	env := worker.RunEnv{WorkDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(env.WorkDir, "inventory.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&Runner{}).materializeInventory(runJob(t), env); err == nil {
		t.Error("an unusable inventory path must fail")
	}
}

func TestMaterializeRunnerConfigWritesExtravars(t *testing.T) {
	env := runEnv(t)
	job := runJob(t)
	job.ExtraVars = map[string]string{"count": "2", "msg": `"hi"`}
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	text := string(mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")))
	if !strings.Contains(text, `"count":2`) || !strings.Contains(text, `"msg":"hi"`) {
		t.Errorf("extravars = %q", text)
	}
	cfg := string(mustRead(t, filepath.Join(env.WorkDir, "ansible.cfg")))
	for _, want := range []string{"host_key_checking = True", "retry_files_enabled = False", "ssh_args = -F "} {
		if !strings.Contains(cfg, want) {
			t.Errorf("ansible.cfg lacks %q:\n%s", want, cfg)
		}
	}
}

func TestAdhocPlaybookTextIsYAMLSafe(t *testing.T) {
	// An argv full of YAML-significant characters marshals as a literal
	// block scalar — re-parsing gives the exact command back, and the
	// playbook stays exactly one play: the command text can never inject
	// structure.
	argv := "echo 'hi'; [a]: b\n--- inject"
	text, err := adhocPlaybookText(`{"module":"shell","argv":` + jsonString(argv) + `}`)
	if err != nil {
		t.Fatalf("adhocPlaybookText: %v", err)
	}
	var plays []map[string]any
	if err := yaml.Unmarshal([]byte(text), &plays); err != nil {
		t.Fatalf("the marshalled playbook is not valid YAML: %v\n%s", err, text)
	}
	if len(plays) != 1 {
		t.Fatalf("plays = %d, want exactly one", len(plays))
	}
	tasks, ok := plays[0]["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks = %#v", plays[0]["tasks"])
	}
	task := tasks[0].(map[string]any)
	if got := task["ansible.builtin.shell"]; got != argv {
		t.Errorf("shell = %#v, want the argv verbatim", got)
	}
	if task["changed_when"] != true {
		t.Error("changed_when: true is part of the ad-hoc task shape")
	}
	if _, ok := plays[0]["changed_when"]; ok {
		t.Error("changed_when must not sit at play level: it is not a valid play attribute")
	}
}

// jsonString quotes a test string as JSON.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestMaterializePlaybookInlineWritesFile(t *testing.T) {
	env := runEnv(t)
	job := runJob(t)
	job.Playbook = "- hosts: all\n"
	path, err := (&Runner{}).materializePlaybook(context.Background(), job, env)
	if err != nil {
		t.Fatalf("materializePlaybook: %v", err)
	}
	if path != filepath.Join(env.WorkDir, "project", "playbook.yml") {
		t.Errorf("path = %q", path)
	}
	if got := string(mustRead(t, path)); got != job.Playbook {
		t.Errorf("content = %q", got)
	}
}

func TestMaterializePlaybookFailsClosed(t *testing.T) {
	job := runJob(t)
	job.Playbook = "   "
	if _, err := (&Runner{}).materializePlaybook(context.Background(), job, runEnv(t)); err == nil {
		t.Error("a blank playbook must fail")
	}
}

func TestCredentialSecretErrPaths(t *testing.T) {
	// An unknown credential fails; an empty id is the zero secret.
	if _, err := credentialSecret(nil, "cred-x"); err == nil {
		t.Error("a credential the claim does not carry must fail")
	}
	if s, err := credentialSecret(nil, ""); err != nil || s != (credentials.Secret{}) {
		t.Errorf("an empty credential id = %v, %v", s, err)
	}
}

// jsonUnmarshal decodes JSON into a pointer (a local indirection so the
// edge tests stay honest about the shape they parse).
func jsonUnmarshal(line string, into any) error {
	return json.Unmarshal([]byte(line), into)
}

// discardLogger is a logger that swallows everything (the injected-logger
// assertion compares identity, output is irrelevant).
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestSummarizeMapsAnsiblePlaybookExitCodes: ansible-playbook exits 2 when
// a host failed and 4 when a host was unreachable (TaskQueueManager's
// RUN_FAILED_HOSTS / RUN_UNREACHABLE_HOSTS). Both are runs that happened
// and reported a recap — failed with stats, so retry-failed can re-run the
// unreachable host (a host whose pinned key no longer matches is exactly
// such a host). Other codes without a recap are infrastructure errors.
func TestSummarizeMapsAnsiblePlaybookExitCodes(t *testing.T) {
	conns := map[string]string{"web1": `{"host_id":"` + testHostID + `"}`}
	clean := map[string]hostStat{"web1": {Ok: 2}}
	failed := map[string]hostStat{"web1": {Ok: 1, Failed: 1}}
	dark := map[string]hostStat{"web1": {Unreachable: 1}}
	cases := []struct {
		name            string
		rc              int
		stats           map[string]hostStat
		want            worker.Status
		wantUnreachable bool
	}{
		{"clean", 0, clean, worker.StatusSucceeded, false},
		{"host failures", 2, failed, worker.StatusFailed, false},
		{"unreachable host", 4, dark, worker.StatusFailed, true},
		{"unreachable without a recap", 4, nil, worker.StatusError, false},
		{"ansible error", 1, nil, worker.StatusError, false},
		{"bad options", 5, nil, worker.StatusError, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, stats, _ := summarize(c.rc, c.stats, nil, "", conns, 0)
			if status != c.want {
				t.Fatalf("summarize(%d) = %s, want %s", c.rc, status, c.want)
			}
			got, _ := stats[statsUnreachableHosts].([]string)
			if c.wantUnreachable && (len(got) != 1 || got[0] != testHostID) {
				t.Fatalf("unreachable_hosts = %v, want [%s]", stats[statsUnreachableHosts], testHostID)
			}
		})
	}
}

// TestSummarizeNamesAFailedExtraVarsLoad is backlog 2.71(b). The
// extra-vars file carries a guard key YAML cannot parse, so a document
// Ansible fails to load as JSON fails the run instead of silently
// falling back to YAML and dropping every unsafe marker. What the
// operator then saw was "ansible-runner exited with code 1" — the
// stderr, carrying Ansible's own YAML complaint about the guard, was
// dropped on this branch — and in the run log, a parse error pointing
// at the guard's column with the hint "Colons in unquoted values must
// be followed by a non-space character". Neither names the real cause:
// the run's extra vars are not a document Ansible can load.
func TestSummarizeNamesAFailedExtraVarsLoad(t *testing.T) {
	guardErr := "ERROR! We were unable to read either as JSON nor YAML, these are the errors we got from each:\n" +
		"JSON: Expecting value: line 1 column 1 (char 0)\n" +
		"Syntax Error while loading YAML.\n  mapping values are not allowed in this context\n\n" +
		"The error appears to be in '/work/env/extravars': line 1, column 28\n" +
		`{"pulsix_extravars_guard": {"json-only: this key is longer than …`

	status, stats, errText := summarize(1, nil, nil, guardErr, nil, 0)
	if status != worker.StatusError || stats != nil {
		t.Fatalf("summarize = %s, %v; want an error with no stats", status, stats)
	}
	if !strings.Contains(errText, "extra vars") {
		t.Fatalf("errText = %q; it must name the extra vars as the cause, not just the exit code", errText)
	}
	if strings.Contains(errText, "Colons in unquoted values") {
		t.Fatalf("errText = %q; it must not repeat Ansible's misleading hint", errText)
	}

	// An unrelated failure keeps the plain exit-code reason: the guard
	// is the only thing this branch recognises.
	_, _, other := summarize(5, nil, nil, "ERROR! the playbook could not be found", nil, 0)
	if strings.Contains(other, "extra vars") {
		t.Fatalf("an unrelated failure reported %q", other)
	}
}
