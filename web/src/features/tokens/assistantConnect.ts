const SCRIPT = '~/.local/bin/assistant-mcp.sh';

/** The commands of the connect guide, `<origin>` filled in. */
export function downloadCommand(origin: string): string {
  return `mkdir -p ~/.local/bin && curl -fsSL ${origin}/assistant-mcp.sh -o ${SCRIPT} && chmod +x ${SCRIPT}`;
}

export function addCommand(origin: string, name: string, orgSlug?: string): string {
  return `${SCRIPT} add ${name} ${origin}${orgSlug ? ` --org ${orgSlug}` : ''}`;
}

export function checkCommand(name: string): string {
  return `${SCRIPT} check ${name}`;
}

/**
 * suggestedName is the name the script registers a server under:
 * `<first label of the host>-<org slug>` (lowercase letters, digits, `-`),
 * or `pulsix-<slug>` when the host is an IP address or localhost.
 */
export function suggestedName(origin: string, orgSlug: string): string {
  let host = '';
  try {
    host = new URL(origin).hostname.toLowerCase();
  } catch {
    // An unparsable origin falls back to the generic prefix.
  }
  const isIp = /^\d+\.\d+\.\d+\.\d+$/.test(host) || host.startsWith('[') || host.includes(':');
  const label = isIp || host === 'localhost' ? '' : (host.split('.')[0] ?? '');
  const clean = label.replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '');
  return `${clean || 'pulsix'}-${orgSlug}`;
}
