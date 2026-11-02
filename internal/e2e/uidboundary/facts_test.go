// Facts a managed host returns, proven against the image's ansible-core
// (backlog 7.28).
package uidboundary

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestAManagedHostsFactsCannotReplaceItsPinnedSSHOptions is backlog 7.28.
// The worker moves each host's SSH common args — the pinned host-key
// options and the jump chain — into a hostvar and forces
// ansible_ssh_common_args to a template that reads it back. A module's
// result comes from the managed host, and Ansible injects the facts in it
// as top-level vars, above inventory hostvars. Under a name ansible-core
// does not strip from returned facts, a compromised host could replace its
// own pin on the next connection and add a ProxyCommand that runs on the
// worker. The fixture host here answers every module run with such facts,
// under both the carrier's old name and its current one; the next task
// must still connect with the pinned options, and the ProxyCommand must
// never run.
func TestAManagedHostsFactsCannotReplaceItsPinnedSSHOptions(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)

	// The play uid has to be able to create the marker, as the ssh client
	// running the ProxyCommand would.
	markerDir, err := os.MkdirTemp("", "facts-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(markerDir) })
	if err := os.Chmod(markerDir, 0o777); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(markerDir, "proxycommand-ran")
	forged := "-o StrictHostKeyChecking=no -o ProxyCommand='touch " + marker + "'"
	module, err := json.Marshal(map[string]any{
		"changed": false,
		"ping":    "pong",
		"ansible_facts": map[string]string{
			"pulsix_ssh_common_args":         forged,
			"ansible_ssh_pulsix_common_args": forged,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	clientPEM, clientPub := sshKeyPair(t)
	addr, hostKey := serveSSH(t, clientPub, func(cmd string) (string, bool) {
		// With pipelining, a module run is the interpreter reading the
		// module from stdin; the raw task below is plain sh.
		return string(module), strings.Contains(cmd, "/usr/bin/python3")
	})
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	job := localhostJob(t, `
- hosts: target
  gather_facts: false
  tasks:
    - name: a module whose result the host forges
      ansible.builtin.ping:
    - name: connect again after the forged facts
      ansible.builtin.raw: echo pinned-connection-held
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-00000000004a"
	job.SshUsername = "pulsix"
	job.InventoryDoc = `{"all": {"hosts": ["target"]}, "_meta": {"hostvars": {"target": {` +
		`"ansible_host": "` + host + `", "ansible_port": ` + port + `, "ansible_user": "pulsix", "ansible_pipelining": true, ` +
		`"ansible_python_interpreter": "/usr/bin/python3"}}}}`
	job.Connections = map[string]string{
		"target": `{"host_id":"01900000-0000-7000-8000-0000000000dd","credential_id":"cred-ssh","bastion_source":"none"}`,
	}
	job.Credentials = map[string]string{
		"cred-ssh": `{"private_key": ` + mustJSON(t, clientPEM) + `}`,
	}
	job.HostKeys = map[string]string{"target": ssh.FingerprintSHA256(hostKey)}
	job.KnownHosts = map[string]string{"target": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey)))}

	result, events, logs := runOneJob(t, job)

	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the host's forged ProxyCommand ran on the worker:\n%s\n%s", eventDump(events), logs)
	}
	if got := result.GetStatus(); got != "succeeded" {
		t.Fatalf("status = %q, want succeeded (error: %q):\n%s\n%s", got, result.GetError(), eventDump(events), logs)
	}
	if !eventsContain(events, "pinned-connection-held") {
		t.Errorf("the connection after the forged facts never ran its command:\n%s", eventDump(events))
	}
}
