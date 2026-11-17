const KEY = 'pulsix.lastOrg';

/** Remembers the organization the person is working in, for this tab: the
 * personal settings area is outside any organization, and its "Back" link
 * returns to the one they came from. Storage may be blocked; then nothing
 * is remembered. */
export function rememberOrg(slug: string): void {
  try {
    sessionStorage.setItem(KEY, slug);
  } catch {
    // Not remembering is harmless: Back falls back to the first organization.
  }
}

/** The organization remembered by rememberOrg, if any. */
export function lastOrg(): string | undefined {
  try {
    return sessionStorage.getItem(KEY) ?? undefined;
  } catch {
    return undefined;
  }
}
