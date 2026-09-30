import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { HostChecksCard } from './HostChecksCard.tsx';

/**
 * The chart is the SPA's only lazily loaded chunk, and a chunk that does
 * not arrive is not hypothetical for a self-hosted product: every build
 * changes the file hashes, so an operator whose host page was open across
 * a Pulsix deploy asks for a URL that now 404s the first time they click
 * Graph.
 *
 * React.lazy re-throws that rejection during render. Without a boundary
 * the throw reaches TanStack Router, whose default ErrorComponent replaces
 * the entire route — the page the operator was watching, chrome included —
 * with an unstyled "Something went wrong!"; and because React.lazy caches
 * the rejected promise, it stays that way until a reload.
 *
 * This lives in its own file because the mock has to be hoisted above the
 * card's own import, and every other test in HostChecksCard.test.tsx wants
 * the real chart.
 */
vi.mock('../../ui/MetricChart.tsx', () => {
  throw new Error('Failed to fetch dynamically imported module: /assets/MetricChart-Cf_hbkrc.js');
});

const NOW = '2026-09-27T12:00:00Z';

const CHECKS = {
  host_id: 'h1',
  monitored: true,
  checks: [
    {
      definition_id: 'd1', key: 'cpu', plugin: 'cpu', params: {}, interval_seconds: 60,
      enabled: true, origin: 'seed', source: 'org',
      values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: false, state: null, flapping: false }],
    },
  ],
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks', component: () => <div>CHECKS PAGE</div> });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    component: () => (
      <>
        <h1>web-04.fra</h1>
        <HostChecksCard org="acme" hostId="h1" canRead canWrite />
      </>
    ),
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([checksRoute, hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('HostChecksCard when the chart chunk does not arrive', () => {
  // The boundary reports the throw; React also logs it. Neither is a test
  // failure, and both would otherwise bury the run's real output.
  let errors: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    errors = vi.spyOn(console, 'error').mockImplementation(() => {});
  });
  afterEach(() => {
    errors.mockRestore();
  });

  it('loses the graph, not the page', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/metrics': {
        check: 'cpu', item: '', metric: 'usage_percent', range: '1h', from: NOW, to: NOW,
        points: [{ at: NOW, value: 12.5 }],
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Graph cpu usage_percent' }));

    const fallback = await screen.findByText(/The chart could not be loaded/);
    // Both fallbacks and the chart itself take the same CHART_HEIGHT, so
    // the panel does not jump when one replaces another. jsdom has no
    // layout, so this pins the declared height, not the rendered one.
    expect(fallback).toHaveStyle({ height: '180px' });
    // The router's own error component would have replaced all of this.
    expect(screen.getByRole('heading', { name: 'web-04.fra' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Monitoring' })).toBeInTheDocument();
    expect(screen.getByText('12.5')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Manage checks' })).toBeInTheDocument();
    expect(screen.queryByText(/Something went wrong/)).not.toBeInTheDocument();
    // The range switch stays usable: only the drawing surface is gone.
    expect(screen.getByRole('button', { name: '24 h' })).toBeInTheDocument();
  });
});
