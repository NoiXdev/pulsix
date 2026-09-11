import type { Credential } from '../../api/types.ts';
import type { Tone } from '../../ui/badge.tsx';

export const KIND_LABEL: Record<string, string> = {
  ssh_key: 'SSH key',
  ssh_password: 'SSH password',
  git_key: 'Git deploy key',
  git_token: 'Git token',
};

type CredentialLike = Pick<Credential, 'kind' | 'key_type' | 'used_by' | 'expired' | 'expires_soon' | 'expires_at'>;

export function kindLabel(c: CredentialLike): string {
  const kind = KIND_LABEL[c.kind] ?? c.kind;
  return c.key_type ? `${kind} · ${c.key_type}` : kind;
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

export function usageLabel(c: CredentialLike): string {
  const parts: string[] = [];
  if (c.used_by.hosts) parts.push(plural(c.used_by.hosts, 'host'));
  if (c.used_by.bastions) parts.push(plural(c.used_by.bastions, 'jump host'));
  return parts.join(' · ') || 'unused';
}

/** expiryState is null for a credential that is fine; expiry is a state, so
 * it uses the state colours. */
export function expiryState(c: CredentialLike, now: Date = new Date()): { tone: Tone; text: string } | null {
  if (c.expired) return { tone: 'crit', text: 'expired' };
  if (c.expires_soon && c.expires_at) {
    const days = Math.max(0, Math.ceil((new Date(c.expires_at).getTime() - now.getTime()) / 86_400_000));
    return { tone: 'warn', text: `expires in ${days} d` };
  }
  return null;
}

export function routeLabel(source: string): string {
  if (source.startsWith('group:')) return `inherited from group ${source.slice('group:'.length)}`;
  if (source === 'zone') return 'zone default';
  if (source === 'host') return 'set on this host';
  return 'direct connection';
}
