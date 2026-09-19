package runner

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/ansiblevars"
)

// The claim marks every string a webhook payload supplied as
// {"__ansible_unsafe": "<s>"} (internal/jobs, final review C1). These
// tests pin the worker's half: the marker survives into env/extravars
// unchanged, and — where ansible-playbook is installed — Ansible then
// prints the value literally instead of running the lookup in it.
const (
	unsafeClaimValue = `{"__ansible_unsafe":"{{ lookup('pipe', 'printf %s-%s PWNED 42') }}"}`
	plainClaimValue  = `"{{ lookup('pipe', 'printf %s-%s PLAIN 7') }}"`
)

func TestMaterializeRunnerConfigKeepsTheUnsafeMarker(t *testing.T) {
	env := runEnv(t)
	job := runJob(t)
	job.ExtraVars = map[string]string{"branch": unsafeClaimValue, "plain": plainClaimValue}
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	var vars map[string]any
	if err := json.Unmarshal(mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")), &vars); err != nil {
		t.Fatal(err)
	}
	branch, ok := vars["branch"].(map[string]any)
	if !ok || branch["__ansible_unsafe"] != "{{ lookup('pipe', 'printf %s-%s PWNED 42') }}" {
		t.Fatalf("branch = %#v, want the unsafe marker kept", vars["branch"])
	}

	ansible := requireAnsiblePlaybook(t)
	// Only the two values under test: the forced connection vars in the
	// real file would point a localhost run at SSH.
	sub, err := json.Marshal(map[string]any{"branch": vars["branch"], "plain": vars["plain"]})
	if err != nil {
		t.Fatal(err)
	}
	text := debugWithExtraVars(t, ansible, sub, "branch", "plain")
	if strings.Contains(text, "PWNED-42") || !strings.Contains(text, "branch={{ lookup('pipe', 'printf %s-%s PWNED 42') }}") {
		t.Fatalf("the marked value must print literally, never run:\n%s", text)
	}
	// The control: the same shape without the marker is rendered — the
	// vector the marker closes.
	if !strings.Contains(text, "plain=PLAIN-7") {
		t.Fatalf("the unmarked control value was expected to render:\n%s", text)
	}
}

// TestNestedUntrustedLookupStaysLiteralInRealAnsible is final re-review
// New Breakage 1: a webhook value nested in lists and objects, marked by
// the claim (ansiblevars.Inert) and written by materializeRunnerConfig,
// stays literal text in a real ansible-playbook run. The sender-chosen
// "__ansible_unsafe" key that used to defeat the marker (Ansible's JSON
// load fails, the file is re-read as YAML and every marker is dropped) is
// refused before it can reach the file — at receive time and at claim.
func TestNestedUntrustedLookupStaysLiteralInRealAnsible(t *testing.T) {
	const lookup = "{{ lookup('pipe', 'printf %s-%s NESTED 9') }}"
	payload := map[string]any{"commits": []any{map[string]any{"meta": map[string]any{"message": lookup}}}}
	bypass := map[string]any{"__ansible_unsafe": map[string]any{"__ansible_unsafe": "x"}}
	if err := ansiblevars.CheckUntrustedKeys(bypass); err == nil {
		t.Fatal("the bypass key must be refused for untrusted values")
	}

	ansible := requireAnsiblePlaybook(t)
	claimValue := func(v any) string {
		b, err := json.Marshal(ansiblevars.Inert(v))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	env := runEnv(t)
	job := runJob(t)
	job.ExtraVars = map[string]string{"event": claimValue(payload)}
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	generated := mustRead(t, filepath.Join(env.WorkDir, "env", "extravars"))
	text := debugWithExtraVars(t, ansible, generated, "event.commits[0].meta.message")
	if strings.Contains(text, "NESTED-9") || !strings.Contains(text, "event.commits[0].meta.message="+lookup) {
		t.Fatalf("the nested marked value must print literally, never run:\n%s", text)
	}

	// The control shows why the guard is load-bearing: with the refused
	// key next to it, the same marked value is templated (ansible-core
	// 2.21.4). Logged, not asserted, so a fixed Ansible does not fail CI.
	job.ExtraVars["other"] = claimValue(map[string]any{"obj": bypass})
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	control := debugWithExtraVars(t, ansible, mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")), "event.commits[0].meta.message")
	t.Logf("with the refused key present, the lookup ran: %v", strings.Contains(control, "NESTED-9"))
}

// requireAnsiblePlaybook returns the ansible-playbook binary. It skips when
// the binary is missing, unless PULSIX_E2E_REAL_ANSIBLE=1 asks for the
// real-Ansible checks — then a missing binary fails the test (CI).
func requireAnsiblePlaybook(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("ansible-playbook")
	if err == nil {
		return bin
	}
	if os.Getenv("PULSIX_E2E_REAL_ANSIBLE") == "1" {
		t.Fatalf("PULSIX_E2E_REAL_ANSIBLE=1 but ansible-playbook is not installed: %v", err)
	}
	t.Skipf("ansible-playbook not installed (set PULSIX_E2E_REAL_ANSIBLE=1 to require it): %v", err)
	return ""
}

// debugWithExtraVars runs a localhost playbook with extraVars as
// "-e @file" (how ansible-runner passes env/extravars) that prints
// "<expr>=<value>" for each expression, and returns the output.
func debugWithExtraVars(t *testing.T, ansible string, extraVars []byte, exprs ...string) string {
	t.Helper()
	dir := t.TempDir()
	evPath, pbPath := filepath.Join(dir, "ev.json"), filepath.Join(dir, "pb.yml")
	if err := os.WriteFile(evPath, extraVars, 0o600); err != nil {
		t.Fatal(err)
	}
	pb := "- hosts: localhost\n  gather_facts: false\n  tasks:\n"
	for _, e := range exprs {
		pb += "    - ansible.builtin.debug:\n        msg: \"" + e + "={{ " + e + " }}\"\n"
	}
	if err := os.WriteFile(pbPath, []byte(pb), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ansible, "-i", "localhost,", "-c", "local", "-e", "@"+evPath, pbPath)
	cmd.Env = append(os.Environ(), "ANSIBLE_NOCOLOR=1", "ANSIBLE_LOCALHOST_WARNING=False", "ANSIBLE_HOME="+filepath.Join(dir, "home"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ansible-playbook: %v\n%s", err, out)
	}
	return string(out)
}
