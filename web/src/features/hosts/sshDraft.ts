import type { HostSSHInput } from '../../api/hooks/useSsh.ts';
import type { Bastion, HostSSH } from '../../api/types.ts';

export type JumpMode = HostSSH['bastion_mode'];

/** SshDraft is what the SSH access dialog edits: the host's address (a
 * field of the host itself) and its SSH settings, as typed. */
export interface SshDraft {
  address: string;
  port: string;
  username: string;
  credentialId: string;
  mode: JumpMode;
  bastionId: string;
}

export function draftOf(address: string, ssh: HostSSH): SshDraft {
  return {
    address,
    port: String(ssh.port),
    username: ssh.username,
    credentialId: ssh.credential_id ?? '',
    mode: ssh.bastion_mode,
    bastionId: ssh.bastion_id ?? '',
  };
}

/** sshBody is the draft as the SSH route takes it: empty fields left out,
 * the jump host only when it is the choice. */
export function sshBody(d: SshDraft): HostSSHInput {
  const body: HostSSHInput = { port: Number(d.port.trim()) || 22, bastion_mode: d.mode };
  if (d.username.trim()) body.username = d.username.trim();
  if (d.credentialId) body.credential_id = d.credentialId;
  if (d.mode === 'explicit' && d.bastionId) body.bastion_id = d.bastionId;
  return body;
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

export function sshChanged(a: SshDraft, b: SshDraft): boolean {
  return !same(sshBody(a), sshBody(b));
}

export function addressChanged(a: SshDraft, b: SshDraft): boolean {
  return a.address.trim() !== b.address.trim();
}

/** sameDraft compares what a save would write, not the keystrokes. */
export function sameDraft(a: SshDraft, b: SshDraft): boolean {
  return !addressChanged(a, b) && !sshChanged(a, b);
}

/** draftProblem names the field that keeps the draft from being saved. */
export function draftProblem(d: SshDraft): 'port' | 'bastion' | null {
  const port = d.port.trim();
  if (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535) return 'port';
  if (d.mode === 'explicit' && !d.bastionId) return 'bastion';
  return null;
}

type BastionLike = Pick<Bastion, 'id' | 'name' | 'via_bastion_id'>;

/** bastionChain names the hops to the jump host `id`, outermost first: one
 * reached through another (via_bastion_id) comes after it. A loop ends the
 * chain; an unknown id is named by a piece of itself. */
export function bastionChain(id: string, bastions: readonly BastionLike[]): string[] {
  const names: string[] = [];
  const seen = new Set<string>();
  let cur: string | undefined = id;
  while (cur && !seen.has(cur)) {
    seen.add(cur);
    const at: string = cur;
    const b = bastions.find((x) => x.id === at);
    names.unshift(b?.name ?? at.slice(0, 8));
    cur = b?.via_bastion_id;
  }
  return names;
}

export interface DraftRoute {
  /** Where the route comes from, as the inventory preview says it
   * (`bastion_source`, see routeLabel). */
  source: string;
  hops: string[];
  /** Only the zone's default is known: an inventory group may still
   * override it. */
  approximate: boolean;
}

/**
 * draftRoute is the route a draft's jump host choice leads to. Inherit is
 * known exactly only while the host inherits today — the inventory preview
 * has resolved it, groups included; otherwise the zone's default is the
 * best guess.
 */
export function draftRoute({
  mode, bastionId, savedMode, resolved, zoneBastionId, bastions,
}: {
  mode: JumpMode;
  bastionId: string;
  savedMode: JumpMode;
  resolved?: { source: string; names: string[] };
  zoneBastionId?: string;
  bastions: readonly BastionLike[];
}): DraftRoute {
  if (mode === 'none') return { source: 'none', hops: [], approximate: false };
  if (mode === 'explicit') return { source: 'host', hops: bastionId ? bastionChain(bastionId, bastions) : [], approximate: false };
  if (savedMode === 'inherit' && resolved) return { source: resolved.source, hops: resolved.names, approximate: false };
  if (zoneBastionId) return { source: 'zone', hops: bastionChain(zoneBastionId, bastions), approximate: true };
  return { source: 'none', hops: [], approximate: true };
}

export function sshTarget(user: string, address: string, port: string | number): string {
  return `${user ? `${user}@` : ''}${address}:${port}`;
}

/** effectiveUser is the login name Pulsix uses: the one set on the host,
 * else the credential's own, else none. */
export function effectiveUser(username: string | undefined, credentialUsername: string | undefined): string {
  return username?.trim() || credentialUsername || '';
}
