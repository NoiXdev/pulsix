import '@testing-library/jest-dom/vitest';
import { fetchMock } from './fetchMock.ts';
// A fourth browser API jsdom does not ship, alongside the three at the
// bottom of this file: ResizeObserver, which ui/MetricChart.tsx uses to
// follow its container's width (backlog 3.26). Without a constructor the
// component throws on mount in every test that reaches it, including the
// page tests that render it only indirectly. The stub installs itself on
// import; the `resizeTo` helper tests drive it with lives beside it.
import './resizeObserver.ts';

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

// ---------------------------------------------------------------------------
// uPlot (ui/MetricChart.tsx) needs three browser APIs jsdom does not ship.
// All three are stubbed here rather than per test file, because uPlot reads
// the first of them at *module load* time — before any test body runs — and
// because a chart that is only mounted indirectly (HostChecksCard) would
// otherwise need the same block copied into every page test that reaches it.
// ---------------------------------------------------------------------------

// 1. matchMedia: uPlot's setPxRatio() subscribes to a
//    "(min-resolution: Xdppx)" query at import time to follow
//    devicePixelRatio changes. jsdom implements no media queries at all, so
//    importing uplot throws "matchMedia is not a function" before the
//    component under test is ever rendered. Nothing matches (dpr stays 1),
//    which is what a test wants anyway.
if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

// 2. A 2D canvas context. jsdom's getContext() returns null (and logs "Not
//    implemented") unless the native `canvas` package is installed — a
//    build-tools dependency Pulsix has no other use for. uPlot draws on the
//    next microtask after construction, so the null context surfaces as an
//    *unhandled* "Cannot read properties of null (reading 'clearRect')"
//    that fails the run without failing a test. This proxy accepts every
//    drawing call, remembers every property written to it (uPlot reads
//    `ctx.font`, `ctx.strokeStyle` etc. back for its own caching) and
//    answers the two getters it needs a real value from. It records
//    nothing: the assertions in MetricChart.test.tsx are on the DOM uPlot
//    builds (wrapper, axes, canvas), never on the pixels.
function stubCanvasContext(canvas: HTMLCanvasElement) {
  const written = new Map<string | symbol, unknown>();
  return new Proxy(
    {},
    {
      get(_target, prop) {
        if (prop === 'canvas') return canvas;
        if (written.has(prop)) return written.get(prop);
        if (prop === 'measureText') return () => ({ width: 0 });
        if (prop === 'getLineDash') return () => [];
        return () => undefined;
      },
      set(_target, prop, value) {
        written.set(prop, value);
        return true;
      },
    },
  );
}
//    It answers **only** for '2d'. Returning this 2D-shaped proxy for
//    'webgl' or 'bitmaprenderer' too would make every feature detection in
//    the app (`canvas.getContext('webgl') !== null`) take the wrong branch
//    under test while taking the right one in a browser — a stub that lies
//    about what the platform supports is worse than the missing one.
HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement, contextType: string) {
  return contextType === '2d' ? stubCanvasContext(this) : null;
} as unknown as HTMLCanvasElement['getContext'];

// 3. Path2D: uPlot builds every series line as a Path2D before stroking it.
//    jsdom does not define the constructor, so the first non-empty series
//    would throw "Path2D is not defined" on that same drawing microtask.
class StubPath2D {
  addPath() {}
  arc() {}
  arcTo() {}
  bezierCurveTo() {}
  closePath() {}
  ellipse() {}
  lineTo() {}
  moveTo() {}
  quadraticCurveTo() {}
  rect() {}
  roundRect() {}
}
(globalThis as unknown as { Path2D: unknown }).Path2D = StubPath2D;
