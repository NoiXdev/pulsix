/**
 * A thin client over mailpit's HTTP API (https://mailpit.axllent.org/docs/api-v1/),
 * used by members.spec.ts to read the real invitation e-mail the manager
 * sends through SMTP rather than reaching into the database or stubbing
 * mail delivery — the point of this suite is to prove the whole product,
 * SMTP included, actually works.
 */
import { MAILPIT_HTTP_URL } from './env.ts';

interface MailpitSearchResult {
  messages: Array<{ ID: string; Created: string }>;
}

interface MailpitMessage {
  Text: string;
}

/**
 * waitForMessageTo polls mailpit's search API for the newest message sent
 * to `to` and returns its plaintext body. Polling (not a single check) is
 * necessary because SMTP delivery happens asynchronously on the manager's
 * side, after the API request that triggered it (POST .../invites) has
 * already returned.
 */
export async function waitForMessageTo(to: string, timeoutMs = 15_000): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  let lastCount = -1;
  for (;;) {
    const res = await fetch(`${MAILPIT_HTTP_URL}/api/v1/search?query=${encodeURIComponent(`to:"${to}"`)}`);
    if (res.ok) {
      const body = (await res.json()) as MailpitSearchResult;
      lastCount = body.messages.length;
      if (body.messages.length > 0) {
        // Newest first per mailpit's own ordering; take the most recent in
        // case an earlier test already sent this address mail.
        const newest = body.messages[0];
        const full = await fetch(`${MAILPIT_HTTP_URL}/api/v1/message/${newest.ID}`);
        if (full.ok) {
          const msg = (await full.json()) as MailpitMessage;
          return msg.Text;
        }
      }
    }
    if (Date.now() > deadline) {
      throw new Error(`no mail arrived for ${to} within ${timeoutMs}ms (last search found ${lastCount} messages)`);
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
}

/** extractInviteLink pulls the "Accept: <url>" link out of the invite
 * e-mail body internal/tenancy.Service.Invite sends (see its own
 * fmt.Sprintf call), and the bare token from the end of that URL. */
export function extractInviteLink(body: string): { url: string; token: string } {
  const match = body.match(/Accept:\s*(\S+)/);
  if (!match) throw new Error(`invite e-mail did not contain an "Accept:" link:\n${body}`);
  const url = match[1];
  const token = url.split('/invite/')[1];
  if (!token) throw new Error(`could not extract an invite token from ${url}`);
  return { url, token };
}
