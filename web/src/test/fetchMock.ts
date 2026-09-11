import { vi } from 'vitest';

/**
 * A single, stable mock `fetch` installed onto `globalThis` by
 * src/test/setup.ts before any test file's own imports run. This has to
 * be a persistent function reference, not something a test replaces with
 * `vi.stubGlobal('fetch', ...)` per test: openapi-fetch's createClient
 * (src/api/client.ts) reads `globalThis.fetch` exactly once, at module
 * load time (`fetch: baseFetch = globalThis.fetch` — see
 * node_modules/openapi-fetch/src/index.js), and caches it in a closure.
 * A later `vi.stubGlobal` swaps the *global* but has no effect on that
 * already-captured reference, so every test that needs to drive the real
 * `api` singleton through a hook or page must instead reconfigure this
 * same function's implementation (see src/test/mockApi.ts) rather than
 * replace it.
 */
export const fetchMock = vi.fn<typeof fetch>();
