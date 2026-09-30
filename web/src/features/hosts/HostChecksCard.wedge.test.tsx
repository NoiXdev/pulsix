import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { HostChecksCard } from './HostChecksCard.tsx';

/**
 * A throw from an **already mounted** chart, as opposed to a chunk that
 * never arrived (HostChecksCard.chunk.test.tsx).
 *
 * Recovery from this used to be accidental: switching range produced a
 * loading gap that unmounted the boundary along with the chart. Once both
 * ranges are cached there is no gap — TanStack serves the cached data and
 * refetches behind it — so the boundary stays mounted and its failed state
 * outlives the input that caused it, leaving the fallback on a range that
 * would render perfectly well. That is what resetKey is for.
 */
vi.mock('../../ui/MetricChart.tsx', () => ({
  MetricChart: ({ range }: { range: string }) => {
    if (range === '1h') throw new Error('chart exploded on this series');
    return <div>CHART {range}</div>;
  },
}));

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

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks', component: () => <div>CHECKS PAGE</div> });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    component: () => <HostChecksCard org="acme" hostId="h1" canRead canWrite />,
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

describe('HostChecksCard when a mounted chart throws', () => {
  let errors: MockInstance<typeof console.error>;
  beforeEach(() => {
    errors = vi.spyOn(console, 'error').mockImplementation(() => {});
  });
  afterEach(() => {
    errors.mockRestore();
  });

  it('does not wedge the panel on a range that renders fine', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/metrics': { check: 'cpu', item: '', metric: 'usage_percent', range: '1h', from: NOW, to: NOW, points: [] },
    });
    renderCard();

    // 1 h throws.
    await user.click(await screen.findByRole('button', { name: 'Graph cpu usage_percent' }));
    expect(await screen.findByText(/The chart could not be loaded/)).toBeInTheDocument();

    // 24 h renders — and caches.
    await user.click(screen.getByRole('button', { name: '24 h' }));
    expect(await screen.findByText('CHART 24h')).toBeInTheDocument();

    // Back to the throwing range.
    await user.click(screen.getByRole('button', { name: '1 h' }));
    expect(await screen.findByText(/The chart could not be loaded/)).toBeInTheDocument();

    // And out again. Both ranges are cached now, so there is no loading gap
    // to unmount the boundary: only resetKey gets the chart back.
    await user.click(screen.getByRole('button', { name: '24 h' }));
    expect(await screen.findByText('CHART 24h')).toBeInTheDocument();
    expect(screen.queryByText(/The chart could not be loaded/)).not.toBeInTheDocument();
  });

  it('does not retry on every re-render of the panel around it', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/metrics': { check: 'cpu', item: '', metric: 'usage_percent', range: '1h', from: NOW, to: NOW, points: [] },
    });
    renderCard();

    await user.click(await screen.findByRole('button', { name: 'Graph cpu usage_percent' }));
    expect(await screen.findByText(/The chart could not be loaded/)).toBeInTheDocument();
    const caught = () => errors.mock.calls.filter((call) => call[0] === 'ErrorBoundary caught').length;
    expect(caught()).toBe(1);

    // Re-selecting the range already on screen builds a new selection
    // object and re-renders the panel, without changing which series is
    // being asked for. A resetKey compared by identity rather than by
    // value would reset here, re-throw, and do it again on every poll.
    await user.click(screen.getByRole('button', { name: '1 h' }));

    expect(await screen.findByText(/The chart could not be loaded/)).toBeInTheDocument();
    expect(caught()).toBe(1);
  });
});
