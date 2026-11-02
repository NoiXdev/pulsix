import type { Credential } from '../../api/types.ts';
import type { Tone } from '../../ui/badge.tsx';
import type { Messages } from '../../i18n/types.ts';

/** kindName names a credential kind; a kind this build does not know
 * is shown as the API sent it. */
export function kindName(kind: string, t: Messages): string {
  return (t.credentials.kinds as Record<string, string>)[kind] ?? kind;
}

/** kindOptions lists every kind for a picker, in the catalogue's order. */
export function kindOptions(t: Messages): { value: string; label: string }[] {
  return Object.entries(t.credentials.kinds).map(([value, label]) => ({ value, label }));
}

type CredentialLike = Pick<Credential, 'kind' | 'key_type' | 'used_by' | 'expired' | 'expires_soon' | 'expires_at'>;

export function kindLabel(c: CredentialLike, t: Messages): string {
  const kind = kindName(c.kind, t);
  return c.key_type ? `${kind} · ${c.key_type}` : kind;
}

export function usageLabel(c: CredentialLike, t: Messages): string {
  const parts: string[] = [];
  if (c.used_by.hosts) parts.push(t.credentials.usage.host(c.used_by.hosts));
  if (c.used_by.bastions) parts.push(t.credentials.usage.jumpHost(c.used_by.bastions));
  // Repositories count in any of three places — the checkout's credential,
  // the legacy single vault password slot, or a labelled vault identity.
  // Without this the column said "unused" for a credential a repository
  // still held, and the delete it invited came back refused; the labelled
  // identity set (2.89) reopened the same gap until the server's own count
  // picked it up too.
  if (c.used_by.repos) parts.push(t.credentials.usage.repository(c.used_by.repos));
  // A job template's own labelled vault identity set is a separate
  // reference from any repository's — naming it "repository" here would
  // misdescribe which resource is actually holding the credential.
  if (c.used_by.templates) parts.push(t.credentials.usage.template(c.used_by.templates));
  return parts.join(' · ') || t.credentials.usage.unused;
}

/** expiryState is null for a credential that is fine; expiry is a state, so
 * it uses the state colours. */
export function expiryState(c: CredentialLike, t: Messages, now: Date = new Date()): { tone: Tone; text: string } | null {
  if (c.expired) return { tone: 'crit', text: t.credentials.expiry.expired };
  if (c.expires_soon && c.expires_at) {
    const days = Math.max(0, Math.ceil((new Date(c.expires_at).getTime() - now.getTime()) / 86_400_000));
    return { tone: 'warn', text: t.credentials.expiry.expiresIn(days) };
  }
  return null;
}

export function routeLabel(source: string, t: Messages): string {
  if (source.startsWith('group:')) return t.credentials.route.inheritedFromGroup(source.slice('group:'.length));
  if (source === 'zone') return t.credentials.route.zoneDefault;
  if (source === 'host') return t.credentials.route.host;
  return t.credentials.route.direct;
}
