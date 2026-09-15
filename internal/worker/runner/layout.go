// The run's file layout inside the loop's work dir (the ansible-runner
// private data dir) and the file-writing helpers. Every file the runner
// writes is owner-only (0600, or 0700 for the two executables); parents are
// owner-private directories.
package runner

import (
	"fmt"
	"os"
	"path/filepath"
)

// File and directory names of the run's layout.
const (
	// inventoryDocName is the raw dynamic-inventory JSON the claim ships.
	inventoryDocName = "inventory.json"
	// inventoryWrapperName is the executable wrapper the run passes as
	// --inventory: Ansible reads a dynamic-inventory document through the
	// script inventory plugin; a bare .json source is claimed by the yaml
	// inventory plugin, whose group handling ignores _meta.hostvars.
	inventoryWrapperName = "inventory"
	// sshConfigName is the run's private ssh_config (bastion aliases).
	sshConfigName = "ssh_config"
	// knownHostsName is the run's private known_hosts: accepted host keys
	// never persist beyond the run's wiped work dir.
	knownHostsName = "known_hosts"
	// ansibleCfgName is the run's generated ansible.cfg.
	ansibleCfgName = "ansible.cfg"
	// keysDirName holds the run's per-credential private key files.
	keysDirName = "keys"
	// gitKeysDir / gitTokensDir hold the repo fetch's credential files.
	gitKeysDir   = "gitkeys"
	gitTokensDir = "gittokens"
	// homeDirName is the run's private HOME (ansible scratch: connection
	// sockets, the fact cache).
	homeDirName = "home"
	// localTmpName is ansible's local temp dir for the run.
	localTmpName = "ansible-local"
	// projectDirName is the ansible-runner project dir (the playbook).
	projectDirName = "project"
	// envDirName is the ansible-runner env dir (extra vars).
	envDirName = "env"
	// extraVarsName is the extra vars document ansible-runner feeds as
	// -e @env/extravars.
	extraVarsName = "extravars"
	// playbookFileName is the materialized inline/adhoc playbook's name.
	playbookFileName = "playbook.yml"
)

// writeTextFile writes a run file: parents are owner-private directories,
// the file gets exactly mode (0600 regular, 0700 executable).
func writeTextFile(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// writeSecretFile writes a run file that carries credential material. Same
// mechanics as writeTextFile; the distinct name marks the files that must
// never be read, logged or copied elsewhere by anything but the subprocess
// that the env points at them.
func writeSecretFile(path, content string, mode os.FileMode) error {
	return writeTextFile(path, content, mode)
}

// sshArgs composes the value of ANSIBLE_SSH_ARGS and the generated
// ansible.cfg's ssh_connection.ssh_args: the run's private ssh_config
// (per-bastion identity files) plus the pinning posture. IdentitiesOnly
// stops the agent's keys from overriding a host's own identity; host keys
// are remembered only in the run's wiped work dir.
func sshArgs(workDir string) string {
	return fmt.Sprintf("-F %s -o IdentitiesOnly=yes -o UserKnownHostsFile=%s",
		filepath.Join(workDir, sshConfigName), filepath.Join(workDir, knownHostsName))
}

// ansibleCfgText renders the run's ansible.cfg.
func ansibleCfgText(workDir string) string {
	return fmt.Sprintf(`[defaults]
host_key_checking = False
retry_files_enabled = False
local_tmp = %s

[ssh_connection]
ssh_args = %s
`, filepath.Join(workDir, localTmpName), sshArgs(workDir))
}

// inventoryWrapperText renders the executable dynamic-inventory wrapper:
// it cats the claim's inventory document. The path is worker-internal
// (MkdirTemp-shaped: no spaces, no quotes).
func inventoryWrapperText(docPath string) string {
	return "#!/bin/sh\nexec cat '" + docPath + "'\n"
}
