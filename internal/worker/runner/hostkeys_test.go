// Host-key pins enforced on ansible's own connections (final review C2),
// the worker half: every pinned host's key lands in the run's private
// known_hosts under a per-host alias, and the host's SSH arguments demand
// strict checking against exactly that entry — a changed key fails the
// connection instead of being accepted. Unpinned hosts and bastion hops
// fall back to trust on first use inside the run's wiped known_hosts
// (documented decision), and nothing ever reads the system known_hosts.
package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

const testPinnedPub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPINNEDKEYPINNEDKEYPINNEDKEYPINNEDKEYxx"

func TestPinHosts(t *testing.T) {
	conn := `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastion_source":"none"}`
	cases := []struct {
		name       string
		hostKeys   map[string]string
		knownHosts map[string]string
		args       string // the host's rendered ansible_ssh_common_args ("" = none)
		wantArgs   string
		wantLines  string
		wantErr    bool
	}{
		{
			name:     "pinned host gets strict checking against its alias",
			hostKeys: map[string]string{"web1": "SHA256:x"}, knownHosts: map[string]string{"web1": testPinnedPub},
			wantArgs:  "-o StrictHostKeyChecking=yes -o HostKeyAlias=pulsix-host-" + testHostID,
			wantLines: "pulsix-host-" + testHostID + " " + testPinnedPub + "\n",
		},
		{
			name:     "strict options come first, ahead of the rendered jump chain",
			hostKeys: map[string]string{"web1": "SHA256:x"}, knownHosts: map[string]string{"web1": testPinnedPub},
			args:      "-o ProxyJump=pulsix-jump-b1",
			wantArgs:  "-o StrictHostKeyChecking=yes -o HostKeyAlias=pulsix-host-" + testHostID + " -o ProxyJump=pulsix-jump-b1",
			wantLines: "pulsix-host-" + testHostID + " " + testPinnedPub + "\n",
		},
		{
			name:     "a pin without its key fails closed",
			hostKeys: map[string]string{"web1": "SHA256:x"}, knownHosts: map[string]string{},
			wantErr: true,
		},
		{
			name:     "a key that is not one authorized-key line fails closed",
			hostKeys: map[string]string{"web1": "SHA256:x"}, knownHosts: map[string]string{"web1": "ssh-ed25519 AAAA\nevil-host ssh-ed25519 BBBB"},
			wantErr: true,
		},
		{
			name:     "unpinned host is untouched",
			hostKeys: map[string]string{}, knownHosts: map[string]string{},
			args: "-o ProxyJump=pulsix-jump-b1", wantArgs: "-o ProxyJump=pulsix-jump-b1", wantLines: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vars := map[string]any{"ansible_host": "192.0.2.10"}
			if c.args != "" {
				vars["ansible_ssh_common_args"] = c.args
			}
			doc := map[string]any{"_meta": map[string]any{"hostvars": map[string]any{"web1": vars}}}
			lines, err := pinHosts(doc, map[string]string{"web1": conn}, c.hostKeys, c.knownHosts)
			if c.wantErr {
				if err == nil {
					t.Fatal("pinHosts must fail closed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := varsOf(t, doc, "web1")["ansible_ssh_common_args"].(string)
			if got != c.wantArgs {
				t.Errorf("common args = %q, want %q", got, c.wantArgs)
			}
			if lines != c.wantLines {
				t.Errorf("known_hosts = %q, want %q", lines, c.wantLines)
			}
		})
	}
}

func TestRunWritesKnownHostsAndEnforcesHostKeyChecking(t *testing.T) {
	fake := writeFakeRunner(t)
	envDump := filepath.Join(t.TempDir(), "env")
	t.Setenv(fakeEnvDump, envDump)
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")

	job := runJob(t)
	job.HostKeys = map[string]string{"app1": "SHA256:pinned"}
	job.KnownHosts = map[string]string{"app1": testPinnedPub}
	env := runEnv(t)
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	kh := filepath.Join(env.WorkDir, "known_hosts")
	checkMode(t, kh, 0o600)
	raw, err := os.ReadFile(kh)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pulsix-host-" + appUUID + " " + testPinnedPub + "\n"; string(raw) != want {
		t.Fatalf("known_hosts = %q, want %q", raw, want)
	}
	inv, err := os.ReadFile(filepath.Join(env.WorkDir, "inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inv), "StrictHostKeyChecking=yes -o HostKeyAlias=pulsix-host-"+appUUID) {
		t.Fatalf("the pinned host's args do not demand strict checking: %s", inv)
	}

	envText, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ANSIBLE_HOST_KEY_CHECKING=True", "GlobalKnownHostsFile=/dev/null", "UserKnownHostsFile=" + kh} {
		if !strings.Contains(string(envText), want) {
			t.Errorf("the run env lacks %q", want)
		}
	}
	if strings.Contains(string(envText), "ANSIBLE_HOST_KEY_CHECKING=False") {
		t.Error("the run disables ansible's host key checking")
	}
	cfg, err := os.ReadFile(filepath.Join(env.WorkDir, "ansible.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "host_key_checking = True") {
		t.Errorf("ansible.cfg = %s, want host key checking on", cfg)
	}
	sshCfg, err := os.ReadFile(filepath.Join(env.WorkDir, "ssh_config"))
	if err != nil {
		t.Fatal(err)
	}
	// Every hop and every unlisted host defaults to the run's own
	// known_hosts, never the system's.
	for _, want := range []string{"Host *\n", "UserKnownHostsFile " + kh, "GlobalKnownHostsFile /dev/null", "StrictHostKeyChecking accept-new"} {
		if !strings.Contains(string(sshCfg), want) {
			t.Errorf("ssh_config lacks %q:\n%s", want, sshCfg)
		}
	}
}

func TestRunFailsClosedOnAnUnenforceablePin(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")
	job := runJob(t)
	job.HostKeys = map[string]string{"app1": "SHA256:pinned"} // no known_hosts entry
	if status, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, runEnv(t), func(*workerpb.RunEvent) error { return nil }); err == nil {
		t.Fatalf("Run = %s, nil; want a closed failure before anything runs", status)
	}
}

// TestRunForcesTheConnectionSecurityVars (re-review N1): extra vars carry
// ansible's highest precedence, so the run writes the SSH arguments, host
// key checking and each host's pinned common args as forced extra vars —
// overriding inventory, play and task vars — and moves the rendered
// per-host common args into pulsix_ssh_common_args, which the forced
// ansible_ssh_common_args references. A claim whose own extra vars try to
// set any of them fails closed.
func TestRunForcesTheConnectionSecurityVars(t *testing.T) {
	fake := writeFakeRunner(t)
	t.Setenv(fakeEnvDump, filepath.Join(t.TempDir(), "env"))
	t.Setenv(fakeEnvEvents, fixtureAbs(t))
	t.Setenv(fakeEnvExit, "0")
	job := runJob(t)
	job.HostKeys = map[string]string{"app1": "SHA256:pinned"}
	job.KnownHosts = map[string]string{"app1": testPinnedPub}
	env := runEnv(t)
	if _, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), job, env, func(*workerpb.RunEvent) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var extra map[string]any
	if err := json.Unmarshal(mustRead(t, filepath.Join(env.WorkDir, "env", "extravars")), &extra); err != nil {
		t.Fatal(err)
	}
	if extra["ansible_ssh_args"] != sshArgs(env.WorkDir) || extra["ansible_host_key_checking"] != true ||
		extra["ansible_ssh_host_key_checking"] != true || extra["ansible_ssh_extra_args"] != "" ||
		extra["ansible_ssh_common_args"] != "{{ pulsix_ssh_common_args | default('') }}" || extra["run"] != "smoke" {
		t.Fatalf("extravars = %v", extra)
	}
	var doc struct {
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(mustRead(t, filepath.Join(env.WorkDir, "inventory.json")), &doc); err != nil {
		t.Fatal(err)
	}
	app := doc.Meta.Hostvars["app1"]
	if _, has := app["ansible_ssh_common_args"]; has {
		t.Fatalf("hostvars still carry ansible_ssh_common_args: %v", app)
	}
	if s, _ := app["pulsix_ssh_common_args"].(string); !strings.HasPrefix(s, "-o StrictHostKeyChecking=yes -o HostKeyAlias=pulsix-host-"+appUUID) {
		t.Fatalf("pulsix_ssh_common_args = %v", app["pulsix_ssh_common_args"])
	}
	if s, _ := doc.Meta.Hostvars["web1"]["pulsix_ssh_common_args"].(string); !strings.Contains(s, "ProxyJump=pulsix-jump-b1") {
		t.Fatalf("web1 lost its jump chain: %v", doc.Meta.Hostvars["web1"])
	}

	for _, name := range []string{"ansible_ssh_args", "ansible_host_key_checking", "pulsix_ssh_common_args"} {
		bad := runJob(t)
		bad.ExtraVars[name] = `"-o StrictHostKeyChecking=no"`
		if status, _, err := (&Runner{RunnerBin: fake}).Run(context.Background(), bad, runEnv(t), func(*workerpb.RunEvent) error { return nil }); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("a claim with extra var %s = %s, %v; want a closed failure naming it", name, status, err)
		}
	}
}
