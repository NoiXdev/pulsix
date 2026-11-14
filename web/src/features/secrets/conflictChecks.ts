import { ApiError } from '../../api/client.ts';

/** conflictChecks reads the check keys out of the store's refusal to
 * delete a secret in use: a 409 with one error detail per check, located
 * at used_by, the key as its message (internal/api/org_secret_handlers.go).
 * Anything else is null, and the caller shows the server's text as it is. */
export function conflictChecks(err: unknown): string[] | null {
  if (!(err instanceof ApiError) || err.status !== 409) return null;
  const body = err.body as { errors?: unknown } | null;
  if (!body || !Array.isArray(body.errors)) return null;
  const keys = body.errors.flatMap((e: unknown) => {
    const d = e as { location?: unknown; message?: unknown } | null;
    return d && d.location === 'used_by' && typeof d.message === 'string' ? [d.message] : [];
  });
  return keys.length > 0 ? keys : null;
}
