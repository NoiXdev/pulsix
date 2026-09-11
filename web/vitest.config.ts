import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import path from 'node:path';

// vitest.config.ts is intentionally separate from vite.config.ts (rather
// than a merged `test` block in the app config): @tailwindcss/vite has no
// reason to run under jsdom for unit tests, and keeping the two configs
// apart means a future change to the build pipeline can't silently change
// what the test runner does.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, 'src'),
    },
  },
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
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      // Scoped to the logic-bearing surface this task's Step 2 names and
      // the small modules around it (the API client's error handling, the
      // codec, the utility helpers) — not the whole src tree. Route
      // components, *Page.tsx compositions, and the generated client are
      // excluded deliberately: they are almost entirely hooks-to-JSX
      // wiring with no independent branching logic of their own to unit
      // test, and Task 15's second half (Playwright, not built here)
      // exercises them against a real backend and a real browser, which is
      // the level that actually matters for a page's correctness. Folding
      // their line count into this threshold would either force
      // shallow snapshot-style tests that regress silently, or force the
      // threshold down for the whole app — neither makes the number more
      // meaningful. See task-15a-report.md for the full rationale.
      include: [
        'src/features/**/*.ts',
        'src/features/hosts/capabilities.tsx',
        'src/ui/shell.tsx',
        'src/api/client.ts',
        'src/lib/utils.ts',
      ],
      exclude: ['src/**/*.test.{ts,tsx}'],
      thresholds: {
        lines: 70,
        statements: 70,
        functions: 70,
        branches: 70,
      },
    },
  },
});
