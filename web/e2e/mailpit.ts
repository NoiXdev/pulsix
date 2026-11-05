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
  Subject: string;
  Text: string;
}

/** mailTo returns every message sent to `to`, newest first (mailpit's own
 * ordering), with subject and plaintext body; an empty list when the search
 * itself failed. */
async function mailTo(to: string): Promise<MailpitMessage[]> {
  const res = await fetch(`${MAILPIT_HTTP_URL}/api/v1/search?query=${encodeURIComponent(`to:"${to}"`)}`);
  if (!res.ok) return [];
  const found = (await res.json()) as MailpitSearchResult;
  const messages: MailpitMessage[] = [];
  for (const { ID } of found.messages) {
    const full = await fetch(`${MAILPIT_HTTP_URL}/api/v1/message/${ID}`);
    if (full.ok) messages.push((await full.json()) as MailpitMessage);
  }
  return messages;
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
  for (;;) {
    const messages = await mailTo(to);
    if (messages.length > 0) return messages[0].Text;
    if (Date.now() > deadline) {
      throw new Error(`no mail arrived for ${to} within ${timeoutMs}ms`);
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
}

/**
 * waitForMessageMatching polls like waitForMessageTo, but returns the
 * plaintext body of the newest message to `to` whose subject or body
 * matches `pattern`: for an address that was already sent other mail (a
 * notice, an earlier link) before the one the test is waiting for.
 */
export async function waitForMessageMatching(to: string, pattern: RegExp, timeoutMs = 15_000): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  let newest = '(no mail)';
  for (;;) {
    const messages = await mailTo(to);
    if (messages.length > 0) newest = `${messages[0].Subject}\n${messages[0].Text}`;
    const hit = messages.find((m) => pattern.test(m.Subject) || pattern.test(m.Text));
    if (hit) return hit.Text;
    if (Date.now() > deadline) {
      throw new Error(`no mail to ${to} matched ${pattern} within ${timeoutMs}ms; the newest was:\n${newest}`);
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
  const token = url.split('/invite#token=')[1];
  if (!token) throw new Error(`could not extract an invite token from ${url}`);
  return { url, token };
}
