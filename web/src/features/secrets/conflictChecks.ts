import { ApiError } from '../../api/client.ts';

/** conflictChecks reads the check keys out of the store's refusal to
 * delete a secret in use (409 "secret in use: the checks a, b use it",
 * internal/orgsecrets), so the page can say it in the GUI's language. Any
 * other error, or a 409 of another shape, is null: the caller then shows
 * the server's text as it is. */
export function conflictChecks(err: unknown): string | null {
  if (!(err instanceof ApiError) || err.status !== 409) return null;
  return /the checks (.+) use it$/.exec(err.message)?.[1] ?? null;
}
