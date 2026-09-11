import '@testing-library/jest-dom/vitest';
import { fetchMock } from './fetchMock.ts';

// Installed before any test file's own imports run, so src/api/client.ts's
// createClient() — which reads globalThis.fetch exactly once, at module
// load time — captures this mock instead of jsdom's real fetch. See
// fetchMock.ts's own doc comment for why a later per-test
// vi.stubGlobal('fetch', ...) cannot substitute for this.
globalThis.fetch = fetchMock as unknown as typeof fetch;

// src/api/client.ts's baseUrl is deliberately '' (the SPA and API always
// share an origin — see that file's own comment), which is why every
// request path is relative ("/api/v1/..."). A real browser resolves a
// relative URL passed to fetch()/Request() against the page's own
// location automatically; Node's built-in fetch/Request (what jsdom test
// runs actually use — jsdom itself implements neither) do not have a
// "current page" to resolve against and throw "Failed to parse URL from
// ..." for a bare relative path. This wraps the global Request
// constructor so a relative input resolves against the same origin
// vitest.config.ts's environmentOptions.jsdom.url sets, matching what a
// real browser would do — and, like the fetch mock above, has to be
// installed here (before src/api/client.ts's createClient() captures
// globalThis.Request) rather than per-test.
const OriginalRequest = globalThis.Request;
class TestRequest extends OriginalRequest {
  constructor(input: RequestInfo | URL, init?: RequestInit) {
    if (typeof input === 'string' && input.startsWith('/')) {
      super(new URL(input, 'http://localhost:3000/'), init);
    } else {
      super(input, init);
    }
  }
}
globalThis.Request = TestRequest as unknown as typeof Request;

// jsdom does not implement <dialog>'s showModal()/close() (ui/dialog.tsx
// uses the native element deliberately — see its own doc comment — no
// Radix dependency needed). A minimal polyfill: showModal sets the `open`
// attribute the same way the browser does, and close does the reverse
// plus fires the native "close" event ui/dialog.tsx's onClose listens for.
HTMLDialogElement.prototype.showModal = function () {
  this.setAttribute('open', '');
};
HTMLDialogElement.prototype.close = function () {
  this.removeAttribute('open');
  this.dispatchEvent(new Event('close'));
};
