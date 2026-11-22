// Backlog 3.126: a claim's check, diff and tags reach ansible-playbook
// through ansible-runner 2.4.3's --cmdline option, never through a shell;
// the recap's per-host counts land in the stats; a secret inside a diff is
// redacted like every other event text.
package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// runArgs runs job against the fake runner and returns its argv.
func runArgs(t *testing.T, job *workerpb.Job) (string, []string) {
	t.Helper()
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, filepath.Join(t.TempDir(), "args"))
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")
	env := runEnv(t)
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return env.WorkDir, strings.Split(strings.TrimSpace(string(mustRead(t, os.Getenv(fakeEnvArgs)))), "\n")
}

func TestRunPassesCheckDiffAndTagsThroughCmdline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		check   bool
		diff    bool
		tags    []string
		cmdline string
	}{
		{"check, diff and tags", true, true, []string{"web", "db:init"}, "--check --diff --tags='web,db:init'"},
		{"check alone", true, false, nil, "--check"},
		{"check with one tag", true, false, []string{"a.b-c_d"}, "--check --tags='a.b-c_d'"},
		{"check with a leading-dash tag", true, false, []string{"-x", "web"}, "--check --tags='-x,web'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := runJob(t)
			job.Check, job.Diff, job.Tags = tc.check, tc.diff, tc.tags
			workDir, args := runArgs(t, job)
			// The option and its value are ONE argv element: argparse reads
			// "--cmdline=<value>" whatever the value starts with, where
			// "--cmdline --check" would take --check for an option of its own.
			assertArgs(t, strings.Join(args, "\n"),
				"run", workDir, "--json",
				"--playbook", filepath.Join(workDir, "project", "playbook.yml"),
				"--inventory", filepath.Join(workDir, "inventory"),
				"--ident", "01900000",
				"--limit", "web*",
				"--cmdline="+tc.cmdline,
			)
		})
	}
}

// TestRunOrdinaryJobHasNoCmdline: the pinned argv of an ordinary run
// (TestRunArgvShapeIsPinned) carries none of the check-mode options.
func TestRunOrdinaryJobHasNoCmdline(t *testing.T) {
	_, args := runArgs(t, runJob(t))
	for _, a := range args {
		if strings.Contains(a, "cmdline") || strings.Contains(a, "--check") || strings.Contains(a, "--diff") || strings.Contains(a, "--tags") {
			t.Fatalf("an ordinary run's argv carries %q: %v", a, args)
		}
	}
}

// TestRunRefusesCheckOnAnotherKind: only a template run can be a dry run;
// a system playbook or an ad-hoc command that arrived with check set is
// an error, never a run that ignores the flag.
func TestRunRefusesCheckOnAnotherKind(t *testing.T) {
	job := runJob(t)
	job.Kind = kindAdhoc
	job.KindPayload = `{"module":"shell","argv":"echo hello"}`
	job.Check = true
	if _, _, err := (&Runner{RunnerBin: writeFakeRunner(t)}).Run(context.Background(), job, runEnv(t), func(*workerpb.RunEvent) error { return nil }); err == nil {
		t.Fatal("an adhoc job with check ran")
	}
}

// TestCmdlineSurvivesShlexSplit: ansible-runner 2.4.3 splits --cmdline
// with Python's shlex.split; every token the worker builds comes back
// whole, quotes and all. The tags travel as ONE token "--tags=<list>", so
// ansible-playbook's argparse reads a list that starts with "-" as the
// option's value instead of an option of its own. Checked against Python
// itself where it exists.
func TestCmdlineSurvivesShlexSplit(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not installed: %v", err)
	}
	for _, tags := range [][]string{
		{"web", "it's", "a b", `back\slash`, `"dq"`},
		{"-x", "web"},
		{"--check"},
	} {
		line := checkCmdline(&workerpb.Job{Check: true, Diff: true, Tags: tags})
		want := []string{"--check", "--diff", "--tags=" + strings.Join(tags, ",")}
		out, err := exec.Command(py, "-I", "-c", "import json, shlex, sys; print(json.dumps(shlex.split(sys.argv[1])))", line).Output()
		if err != nil {
			t.Fatalf("python3 shlex.split: %v", err)
		}
		var got []string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("shlex.split(%q) = %q, want %q", line, got, want)
		}
	}
}

// TestLeadingDashTagsReachArgparseAsAValue: the token form matters to
// ansible-playbook's own parser. argparse refuses "--tags -x" (it reads
// -x as an unknown option) and accepts "--tags=-x"; this pins that the
// worker's tokens are of the accepted form, checked with Python's argparse
// where it exists.
func TestLeadingDashTagsReachArgparseAsAValue(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not installed: %v", err)
	}
	line := checkCmdline(&workerpb.Job{Check: true, Tags: []string{"-x"}})
	script := "import argparse, shlex, sys\n" +
		"p = argparse.ArgumentParser()\n" +
		"p.add_argument('--check', action='store_true')\n" +
		"p.add_argument('--tags', action='append')\n" +
		"print(p.parse_args(shlex.split(sys.argv[1])).tags[0])\n"
	out, err := exec.Command(py, "-I", "-c", script, line).CombinedOutput()
	if err != nil {
		t.Fatalf("argparse refused %q: %v\n%s", line, err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "-x" {
		t.Fatalf("--tags = %q, want -x", got)
	}
}

func TestBuildStatsCarriesPerHostCounts(t *testing.T) {
	line, err := os.ReadFile(fixtureAbs(t))
	if err != nil {
		t.Fatal(err)
	}
	var stats map[string]hostStat
	for _, l := range strings.Split(string(line), "\n") {
		if ev, _, err := ParseEvent([]byte(l)); err == nil && ev.Stats != nil {
			stats = ev.Stats
		}
	}
	if stats == nil {
		t.Fatal("the fixture has no playbook_on_stats event")
	}
	out := buildStats(stats, runJob(t).GetConnections())

	counts, ok := out[statsHostCounts].(map[string]hostCounts)
	if !ok {
		t.Fatalf("host_counts = %#v", out[statsHostCounts])
	}
	want := map[string]hostCounts{
		webUUID: {Ok: 3, Changed: 1, Skipped: 1},
		appUUID: {Failed: 1},
		dbUUID:  {Unreachable: 1},
	}
	if len(counts) != len(want) {
		t.Fatalf("host_counts = %+v, want %+v", counts, want)
	}
	for id, w := range want {
		if counts[id] != w {
			t.Errorf("host_counts[%s] = %+v, want %+v", id, counts[id], w)
		}
	}
	// The lists stay as they were.
	for key, want := range map[string][]string{
		statsFailedHosts: {appUUID}, statsUnreachableHosts: {dbUUID}, statsOKHosts: {webUUID},
		statsChangedHosts: {webUUID}, statsSkippedHosts: {webUUID},
	} {
		if got := out[key].([]string); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	// The wire form: five named counts per host id.
	raw, err := json.Marshal(out[statsHostCounts])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"`+webUUID+`":{"ok":3,"changed":1,"skipped":1,"failed":0,"unreachable":0}`) {
		t.Fatalf("host_counts JSON = %s", raw)
	}
}

// TestRunRedactsSecretsInsideDiffs: --diff puts the before and after of a
// changed file into the task result (res.diff) and into the display text
// of ansible-runner's runner_on_file_diff event. A credential's secret or
// a secret extra var inside either is redacted before the event leaves.
func TestRunRedactsSecretsInsideDiffs(t *testing.T) {
	plusKey := "+" + strings.ReplaceAll(strings.TrimSuffix(leakKey, "\n"), "\n", "\n+")
	line := func(v map[string]any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	diff := []any{map[string]any{
		"before_header": "/etc/app.conf", "after_header": "/etc/app.conf",
		"before": "password: old\n", "after": "password: " + leakPassword + "\ntoken: " + leakExtraVar + "\n" + leakKey,
	}}
	unified := "--- before: /etc/app.conf\n+++ after: /etc/app.conf\n@@ -1 +1,6 @@\n-password: old\n+password: " + leakPassword + "\n+token: " + leakExtraVar + "\n" + plusKey + "\n"
	lines := []string{
		line(map[string]any{"event": "runner_on_file_diff", "stdout": unified, "event_data": map[string]any{"host": "app1", "task": "conf", "diff": diff}}),
		line(map[string]any{"event": "runner_on_ok", "stdout": "changed: [app1]", "event_data": map[string]any{
			"host": "app1", "task": "conf", "res": map[string]any{"changed": true, "diff": diff},
		}}),
		line(map[string]any{"event": "playbook_on_stats", "stdout": "PLAY RECAP", "event_data": map[string]any{
			"ok": map[string]any{"app1": 1}, "changed": map[string]any{"app1": 1}, "failures": map[string]any{}, "dark": map[string]any{}, "skipped": map[string]any{},
		}}),
	}
	fixture := filepath.Join(t.TempDir(), "diff-events.jsonl")
	if err := os.WriteFile(fixture, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvArgs, filepath.Join(t.TempDir(), "args"))
	t.Setenv(fakeEnvEvents, fixture)
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	job.Check, job.Diff = true, true
	job.Credentials["cred-2"] = secretJSON(t, credentials.Secret{PrivateKey: "app-key-material", Password: leakPassword})
	job.Redact = []string{leakExtraVar, leakKey}
	c := &collector{}
	if _, _, err := (&Runner{RunnerBin: writeFakeRunner(t)}).Run(context.Background(), job, runEnv(t), c.sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertStates(t, c.states(), []string{"run_start", "stdout", "changed", "stdout", "run_end"})
	for _, ev := range c.events {
		for _, secret := range []string{leakPassword, leakExtraVar, leakKeyLine} {
			if strings.Contains(ev.GetPayload(), secret) {
				t.Errorf("%s event leaks %q: %s", ev.GetState(), secret, ev.GetPayload())
			}
		}
	}
	// What the run view needs survives: the diff's shape, the redacted
	// values in place.
	var changed struct {
		EventData struct {
			Res struct {
				Diff []map[string]string `json:"diff"`
			} `json:"res"`
		} `json:"event_data"`
	}
	if err := json.Unmarshal([]byte(c.events[2].GetPayload()), &changed); err != nil {
		t.Fatal(err)
	}
	if d := changed.EventData.Res.Diff; len(d) != 1 || !strings.HasPrefix(d[0]["after"], "password: [redacted]\ntoken: [redacted]\n") || d[0]["before"] != "password: old\n" {
		t.Fatalf("res.diff = %+v", changed.EventData.Res.Diff)
	}
	var text struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(c.events[1].GetPayload()), &text); err != nil || !strings.Contains(text.Text, "+password: [redacted]") {
		t.Fatalf("diff display text = %s", c.events[1].GetPayload())
	}
}
