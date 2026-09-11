import { ApiError } from '../api/client.ts';

/** errorStatus pulls the HTTP status out of a thrown query/mutation error,
 * or null when it is not one of our own ApiErrors (a network failure, a
 * thrown non-Error value, ...). Split out of ui/QueryState.tsx so that
 * file exports only components (react-refresh/only-export-components). */
export function errorStatus(error: unknown): number | null {
  return error instanceof ApiError ? error.status : null;
}
