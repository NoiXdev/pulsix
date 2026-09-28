import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['dist', 'coverage', 'src/api/schema.d.ts'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2023,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      ...reactRefresh.configs.vite.rules,
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
    },
  },
  // Backlog 3.24: HostChecksCard loads ui/MetricChart.tsx through React.lazy,
  // which keeps uPlot and the chart out of the entry chunk — 22 kB gzip of the
  // 52 kB lazy chunk, against a 222 kB gzip entry bundle. A static
  // `import { MetricChart } from '.../ui/MetricChart.tsx'` anywhere in the
  // shipped tree would silently undo that, and no test would fail. The
  // selector matches an import *declaration* only, so the `import()`
  // expression React.lazy needs stays legal. Tests are not bundled and import
  // the component directly, so they are exempt.
  // Backlog 3.23: CLAUDE.md puts src/ui at the bottom of the SPA's layering —
  // presentational, no data fetching — and two files had drifted above it by
  // importing hooks out of src/api. Documented is not enforced, so this is
  // the rule. Type-only imports stay legal: src/api/types.ts is the generated
  // schema's vocabulary, and a component that takes `MetricPoint[]` as a prop
  // is presentational precisely because it names the type and fetches nothing.
  {
    files: ['src/ui/**/*.{ts,tsx}'],
    ignores: ['src/ui/**/*.test.{ts,tsx}'],
    rules: {
      '@typescript-eslint/no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: ['**/api/*', '**/api/**'],
              allowTypeImports: true,
              message:
                'src/ui is presentational: reach data through a prop, or put the component in src/features where a hook may live (backlog 3.23).',
            },
          ],
        },
      ],
    },
  },
  {
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/**/*.test.{ts,tsx}'],
    rules: {
      'no-restricted-syntax': [
        'error',
        {
          selector: 'ImportDeclaration[source.value=/ui\\u002fMetricChart/]',
          message:
            'Load MetricChart through React.lazy(() => import(...)) only: a static import pulls uPlot into the entry bundle (backlog 3.24).',
        },
      ],
    },
  },
);
