/** sshCredentials narrows the organization's credentials to what the
 * Default SSH credential field offers: SSH keys and SSH passwords. */
export function sshCredentials<C extends { kind: string }>(all: readonly C[]): C[] {
  return all.filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password');
}
