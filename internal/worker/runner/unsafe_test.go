package runner

import (
	"encoding/json"
	"errors"
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
// refused before it can reach the file — at receive time and at claim —
// and the file's YAML guard fails the run should one get through.
func TestNestedUntrustedLookupStaysLiteralInRealAnsible(t *testing.T) {
	const lookup = "{{ lookup('pipe', 'printf %s-%s NESTED 9') }}"
	payload := map[string]any{"commits": []any{map[string]any{"meta": map[string]any{"message": lookup}}}}
	bypass := map[string]any{"__ansible_unsafe": map[string]any{"__ansible_unsafe": "x"}}
	if err := ansiblevars.CheckUntrusted(bypass); err == nil {
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

	// Should a refused key reach the file anyway, Ansible's JSON load
	// fails; the file's YAML guard (extraVarsGuardVar) then fails the run
	// instead of letting the YAML re-read drop the markers and run the
	// lookup — which is what ansible-core 2.21.4 did before the guard.
	job.ExtraVars["other"] = claimValue(map[string]any{"obj": bypass})
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	control, err := runPlaybookWithExtraVars(t, ansible, mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")), "event.commits[0].meta.message")
	if strings.Contains(control, "NESTED-9") || err == nil {
		t.Fatalf("a refused key in the file must fail the run without running the lookup (err %v):\n%s", err, control)
	}
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
	out, err := runPlaybookWithExtraVars(t, ansible, extraVars, exprs...)
	if err != nil {
		t.Fatalf("ansible-playbook: %v\n%s", err, out)
	}
	return out
}

// runPlaybookWithExtraVars is debugWithExtraVars without the success
// requirement: it returns the output and ansible-playbook's error.
func runPlaybookWithExtraVars(t *testing.T, ansible string, extraVars []byte, exprs ...string) (string, error) {
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
	return string(out), err
}

// TestDeepUntrustedValueCannotDropTheMarkersInRealAnsible is final
// re-review 2, New Breakage 1: from roughly 250 levels of nesting Python's
// recursion limit breaks Ansible's JSON load of the extra-vars file, and
// ansible-core silently re-reads it as YAML (up to roughly 500 levels),
// where every {"__ansible_unsafe": …} marker is a plain dict whose strings
// are templated. The manager caps untrusted nesting at
// ansiblevars.MaxUntrustedDepth; the worker's file additionally carries a
// guard that no YAML parser accepts, so a failed JSON load fails the run
// instead of running anything.
func TestDeepUntrustedValueCannotDropTheMarkersInRealAnsible(t *testing.T) {
	const lookup = "{{ lookup('pipe', 'printf %s-%s DEEP 300') }}"
	var deep any = "leaf"
	for range 300 {
		deep = []any{deep}
	}
	if err := ansiblevars.CheckUntrusted(deep); !errors.Is(err, ansiblevars.ErrTooDeep) {
		t.Fatalf("a 300-level value must be refused as too deep: %v", err)
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
	job.ExtraVars = map[string]string{"branch": claimValue(lookup), "deep": claimValue(deep)}
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	out, err := runPlaybookWithExtraVars(t, ansible, mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")), "branch")
	if strings.Contains(out, "DEEP-300") {
		t.Fatalf("the marked lookup ran after the JSON load failed:\n%s", out)
	}
	if err == nil {
		t.Fatalf("a document Ansible cannot load as JSON must fail the run, not fall back to YAML:\n%s", out)
	}
}

// TestMaterializeRunnerConfigWritesTheYAMLGuard: every extra-vars file
// carries the over-long guard key, and a claim cannot replace it.
func TestMaterializeRunnerConfigWritesTheYAMLGuard(t *testing.T) {
	env := runEnv(t)
	job := runJob(t)
	job.ExtraVars = map[string]string{extraVarsGuardVar: `"off"`}
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	var vars map[string]any
	if err := json.Unmarshal(mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")), &vars); err != nil {
		t.Fatal(err)
	}
	guard, ok := vars[extraVarsGuardVar].(map[string]any)
	if !ok || len(guard) != 1 {
		t.Fatalf("guard = %#v", vars[extraVarsGuardVar])
	}
	for k := range guard {
		if len(k) <= 1024 {
			t.Fatalf("the guard key is %d characters; YAML only refuses implicit keys over 1024", len(k))
		}
	}
}

// TestExtraVarsGuardIsTheFirstKey is final re-review 3, minor 1: when
// Ansible cannot load the file its error quotes the head of line 1, and
// the file is one line. The guard must come first whatever the other
// names sort as, so that head is the guard's own text, never the start of
// a secret value (redaction only replaces whole values).
func TestExtraVarsGuardIsTheFirstKey(t *testing.T) {
	env := runEnv(t)
	job := runJob(t)
	job.ExtraVars = map[string]string{"": `"e"`, "!a": `"x"`, "0": "1", "A": `"y"`, "a_token": `"secret"`, extraVarsGuardVar: `"off"`}
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	raw := mustRead(t, filepath.Join(env.WorkDir, "env", "extravars"))
	prefix := `{"` + extraVarsGuardVar + `":{"` + extraVarsGuardKey + `":true}`
	if !strings.HasPrefix(string(raw), prefix) {
		t.Fatalf("the file must start with the guard, got %.120q", raw)
	}
	var vars map[string]any
	if err := json.Unmarshal(raw, &vars); err != nil {
		t.Fatalf("the file must stay valid JSON: %v", err)
	}
	if vars["a_token"] != "secret" || vars["!a"] != "x" || vars[""] != "e" {
		t.Fatalf("the claim's vars must all survive: %v", vars)
	}
	if strings.Count(string(raw), `"`+extraVarsGuardVar+`"`) != 1 {
		t.Fatal("the guard must appear exactly once")
	}
	// An empty claim still yields a valid document.
	job.ExtraVars = nil
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")), &vars); err != nil {
		t.Fatalf("empty claim: %v", err)
	}
}

// TestUnloadableExtraVarsErrorShowsNoSecretPrefix is final re-review 3,
// minor 1, under the real ansible-playbook: a trusted run whose template
// holds a value Ansible cannot load as JSON fails on the guard, and the
// error's quote of the file shows no prefix of a secret whose name sorts
// before everything else.
func TestUnloadableExtraVarsErrorShowsNoSecretPrefix(t *testing.T) {
	ansible := requireAnsiblePlaybook(t)
	secret := "S3CRET" + strings.Repeat("x", 150)
	env := runEnv(t)
	job := runJob(t)
	job.ExtraVars = map[string]string{
		"a_token": `"` + secret + `"`,
		"cfg":     `{"o":{"__ansible_unsafe":5}}`, // a malformed marker: the JSON load fails
	}
	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	out, err := runPlaybookWithExtraVars(t, ansible, mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")), "cfg")
	if err == nil {
		t.Fatalf("the unloadable file must fail the run:\n%s", out)
	}
	if strings.Contains(out, "S3CRET") {
		t.Fatalf("the error output leaks a secret prefix:\n%s", out)
	}
	if !strings.Contains(out, extraVarsGuardVar) {
		t.Fatalf("expected the error to quote the guard at the head of the file:\n%s", out)
	}
}
