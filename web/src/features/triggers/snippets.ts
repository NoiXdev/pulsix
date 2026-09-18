export interface SnippetInput {
  url: string;
  preset: string;
  auth: string;
  signature_header: string;
  timestamp_header?: string;
}

export interface Snippets {
  curl: string;
  github: string;
  gitlab: string;
  gitea: string;
}

/** snippets renders copy-ready sender configurations. The secret is never
 * embedded: every snippet reads $SECRET, which the operator sets from the
 * once-shown value. No query-string token (spec §3). */
export function snippets(t: SnippetInput): Snippets {
  const ts = t.timestamp_header ?? '';
  let curl: string;
  if (t.auth === 'hmac') {
    const prefix = t.signature_header === 'X-Gitea-Signature' ? '' : 'sha256=';
    const signed = ts ? `printf '%s.%s' "$ts" "$body"` : `printf '%s' "$body"`;
    curl = [
      `body='{"ref":"refs/heads/main"}'`,
      ...(ts ? ['ts=$(date +%s)'] : []),
      `sig=$(${signed} | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')`,
      `curl -X POST ${JSON.stringify(t.url)} \\`,
      `  -H 'Content-Type: application/json' \\`,
      ...(ts ? [`  -H "${ts}: $ts" \\`] : []),
      `  -H "${t.signature_header}: ${prefix}$sig" \\`,
      `  --data "$body"`,
    ].join('\n');
  } else {
    const value = t.signature_header === 'Authorization' ? 'Bearer $SECRET' : '$SECRET';
    curl = [
      `curl -X POST ${JSON.stringify(t.url)} \\`,
      `  -H 'Content-Type: application/json' \\`,
      `  -H "${t.signature_header}: ${value}" \\`,
      `  --data '{"ref":"refs/heads/main"}'`,
    ].join('\n');
  }
  return {
    curl,
    github: `Payload URL: ${t.url}\nContent type: application/json\nSecret: <the trigger secret>\nEvents: Just the push event`,
    gitlab: `URL: ${t.url}\nSecret token: <the trigger secret>\nTrigger: Push events`,
    gitea: `Target URL: ${t.url}\nHTTP method: POST\nPOST content type: application/json\nSecret: <the trigger secret>`,
  };
}
