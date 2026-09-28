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
