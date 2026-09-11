import createClient, { type Middleware } from 'openapi-fetch';
import type { paths } from './schema.d.ts';

/**
 * throwOnError turns any non-2xx response into a thrown ApiError instead of
 * openapi-fetch's default `{ data, error }` result tuple. Every hook in
 * src/api/hooks builds its TanStack Query fetcher on top of this client, so
 * this is the one place that decides how a failed request surfaces — every
 * page gets a thrown error TanStack Query can put in `query.error`/
 * `mutation.error`, instead of every call site having to check `error`
 * itself.
 */
export class ApiError extends Error {
  status: number;
  body: unknown;

  constructor(status: number, body: unknown) {
    super(apiErrorMessage(body, status));
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

function apiErrorMessage(body: unknown, status: number): string {
  if (body && typeof body === 'object' && 'detail' in body && typeof body.detail === 'string') {
    return body.detail;
  }
  if (body && typeof body === 'object' && 'title' in body && typeof body.title === 'string') {
    return body.title;
  }
  return `request failed with status ${status}`;
}

/**
 * rejectIfNotOk is throwOnError's actual logic, pulled out as a plain
 * function so it can be unit-tested directly against a constructed
 * Response — without going through a real (or mocked) `fetch` call, which
 * openapi-fetch resolves once at client-creation time, making it awkward
 * to intercept per-test.
 */
export async function rejectIfNotOk(response: Response): Promise<Response> {
  if (response.ok) return response;
  const clone = response.clone();
  let body: unknown;
  try {
    body = await clone.json();
  } catch {
    body = undefined;
  }
  throw new ApiError(response.status, body);
}

const throwOnError: Middleware = {
  onResponse: ({ response }) => rejectIfNotOk(response),
};

/**
 * api is the one openapi-fetch client instance the whole app shares.
 * baseUrl is empty on purpose: the SPA is always served from the same
 * origin as the API it talks to (internal/web.Handler mounts it as the
 * manager's NotFound handler), so every call is a same-origin,
 * cookie-carrying request with no CORS involved at all.
 *
 * No component or feature module is meant to import this directly — see
 * src/api/hooks/*.ts for the TanStack Query wrappers every page actually
 * uses.
 */
export const api = createClient<paths>({ baseUrl: '', credentials: 'include' });
api.use(throwOnError);

/**
 * unwrap pulls the typed `data` out of an openapi-fetch result. It is safe
 * to assume `data` is present whenever this resolves at all: throwOnError
 * above already turned every non-2xx response into a thrown ApiError
 * before openapi-fetch would otherwise have returned `{ error }`, so the
 * only way execution reaches here is a successful response.
 */
export async function unwrap<T>(promise: Promise<{ data?: T }>): Promise<T> {
  const { data } = await promise;
  return data as T;
}
