package runner

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

	ansible, err := exec.LookPath("ansible-playbook")
	if err != nil {
		t.Skipf("ansible-playbook not installed; the marker's effect is covered by the real-ansible E2E: %v", err)
	}
	// Only the two values under test: the forced connection vars in the
	// real file would point a localhost run at SSH.
	dir := t.TempDir()
	sub, err := json.Marshal(map[string]any{"branch": vars["branch"], "plain": vars["plain"]})
	if err != nil {
		t.Fatal(err)
	}
	evPath, pbPath := filepath.Join(dir, "ev.json"), filepath.Join(dir, "pb.yml")
	if err := os.WriteFile(evPath, sub, 0o600); err != nil {
		t.Fatal(err)
	}
	pb := "- hosts: localhost\n  gather_facts: false\n  tasks:\n" +
		"    - ansible.builtin.debug:\n        msg: \"branch={{ branch }}\"\n" +
		"    - ansible.builtin.debug:\n        msg: \"plain={{ plain }}\"\n"
	if err := os.WriteFile(pbPath, []byte(pb), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ansible, "-i", "localhost,", "-c", "local", "-e", "@"+evPath, pbPath)
	cmd.Env = append(os.Environ(), "ANSIBLE_NOCOLOR=1", "ANSIBLE_LOCALHOST_WARNING=False", "ANSIBLE_HOME="+filepath.Join(dir, "home"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ansible-playbook: %v\n%s", err, out)
	}
	text := string(out)
	if strings.Contains(text, "PWNED-42") || !strings.Contains(text, "branch={{ lookup('pipe', 'printf %s-%s PWNED 42') }}") {
		t.Fatalf("the marked value must print literally, never run:\n%s", text)
	}
	// The control: the same shape without the marker is rendered — the
	// vector the marker closes.
	if !strings.Contains(text, "plain=PLAIN-7") {
		t.Fatalf("the unmarked control value was expected to render:\n%s", text)
	}
}
