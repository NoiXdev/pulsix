import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// vitest.config.ts is intentionally separate from vite.config.ts (rather
// than a merged `test` block in the app config): @tailwindcss/vite has no
// reason to run under jsdom for unit tests, and keeping the two configs
// apart means a future change to the build pipeline can't silently change
// what the test runner does.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    // openapi-fetch resolves the app's relative API paths (baseUrl: '')
    // against window.location, so jsdom needs a real http(s) origin here —
    // its own default ("about:blank" on some versions) is not a valid
    // base for `new URL('/api/...', base)` and every request would fail
    // before src/api/client.ts's own error handling ever runs.
    environmentOptions: { jsdom: { url: 'http://localhost:3000/' } },
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    // web/e2e/*.spec.ts are Playwright specs (import `test`/`expect` from
    // @playwright/test, run by `npm run e2e`, never by Vitest) — without
    // this, Vitest's own default include pattern (`**/*.{test,spec}.*`)
    // matches them too and fails every one of them with "did not expect
    // test() to be called here", since they call Playwright's `test`
    // outside a Playwright-managed suite.
    exclude: ['**/node_modules/**', '**/dist/**', 'e2e/**'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      // Gated on the whole source tree (finding "Important 11",
      // task-15a-report.md fix round 1: the previous five-glob
      // whitelist covered ~100 statements and excluded RootRedirect —
      // where the fix round's Important 3 bug lived — the shell's
      // active-route logic, every schema and every hook). Excluded here,
      // with a reason each:
      //   - main.tsx: pure ReactDOM bootstrap (createRoot().render(...)),
      //     no branching logic of its own — parallel to the Go side's
      //     own cmd/... entry-point exclusion (scripts/coverage-gate.sh).
      //   - router.tsx: declarative route-tree wiring (createRoute calls
      //     naming a component and a path) with no logic of its own to
      //     assert against; every route's *own* component is covered
      //     where it has behaviour (the tests below build small
      //     standalone routers around each one rather than importing
      //     this file, so this file's own statements would otherwise
      //     read 0% for having nothing to do with what was tested).
      //     Still excluded since v0.4f gave it two beforeLoad redirects
      //     (the Settings paths Credentials and Jump hosts vacated):
      //     those are logic, and router.test.tsx navigates the real
      //     router to pin them — the exclusion is about the file's
      //     coverage percentage being meaningless, not about leaving
      //     its behaviour untested.
      //   - api/schema.d.ts, vite-env.d.ts: type-only, no runtime
      //     statements (openapi-typescript-generated and the Vite
      //     ambient-types file respectively).
      //   - test/**: test infrastructure (mockApi, fetchMock, setup),
      //     not application logic.
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.test.{ts,tsx}',
        'src/main.tsx',
        'src/router.tsx',
        'src/api/schema.d.ts',
        'src/vite-env.d.ts',
        'src/test/**',
      ],
      thresholds: {
        lines: 70,
        statements: 70,
        functions: 70,
        branches: 70,
      },
    },
  },
});
