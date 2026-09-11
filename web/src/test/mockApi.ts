import { fetchMock } from './fetchMock.ts';

/**
 * mockApi configures the shared fetch mock (fetchMock.ts) that
 * openapi-fetch's client (src/api/client.ts) is wired to, so a component
 * test can drive real hooks/pages without a real backend. `routes` is
 * keyed "METHOD /api/v1/path" (path only, no query string) and maps to
 * either a fixed JSON body or a function of the parsed request (method,
 * path, search params, and the JSON body if any) returning one — handy
 * for a mutation whose response depends on what was posted. An unmatched
 * request 404s loudly rather than hanging, so a missing mock shows up as
 * a real test failure instead of an infinite "Loading…".
 *
 * openapi-fetch always calls fetch with a pre-built `Request` object (see
 * node_modules/openapi-fetch/src/index.js: `fetch(request, requestInitExt)`),
 * never a bare URL string plus an `init` object — so method, URL and body
 * all have to be read off that Request instance, not off the (usually
 * empty) second argument.
 */
export interface MockRequest {
  method: string;
  path: string;
  search: URLSearchParams;
  body: unknown;
}

export type MockRoute = unknown | ((req: MockRequest) => unknown);

export function mockApi(routes: Record<string, MockRoute>) {
  fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : new Request(input, init);
    const parsed = new URL(request.url, 'http://localhost');
    const method = request.method.toUpperCase();
    const key = `${method} ${parsed.pathname}`;

    let body: unknown;
    const text = await request.clone().text().catch(() => '');
    if (text) {
      try {
        body = JSON.parse(text);
      } catch {
        body = text;
      }
    }

    if (!(key in routes)) {
      return new Response(JSON.stringify({ detail: `mockApi: no route for ${key}` }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      });
    }

    const route = routes[key];
    const resolved =
      typeof route === 'function'
        ? (route as (req: MockRequest) => unknown)({ method, path: parsed.pathname, search: parsed.searchParams, body })
        : route;
    // A route function that wants a non-200 (e.g. simulating a 500) can
    // return a real Response directly instead of a JSON body.
    if (resolved instanceof Response) return resolved;
    return new Response(JSON.stringify(resolved), { status: 200, headers: { 'Content-Type': 'application/json' } });
  });

  return fetchMock;
}
