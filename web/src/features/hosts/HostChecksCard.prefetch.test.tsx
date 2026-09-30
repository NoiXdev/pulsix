import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { HostChecksCard } from './HostChecksCard.tsx';

/**
 * Backlog 3.26a. The chart chunk and the series it draws are two network
 * round trips, and nothing makes them depend on each other: the operator
 * clicks Graph and both could start at once. Rendering `<MetricChart>`
 * only once the series has resolved runs them one after the other, so a
 * cold open costs chunk + request instead of max(chunk, request).
 *
 * The mock counts how often the module is imported at all. It lives in its
 * own file for the same reason the chunk test does: the factory has to be
 * hoisted above the card's import, and every other test in
 * HostChecksCard.test.tsx wants the real chart.
 */
const imported = vi.hoisted(() => ({ count: 0 }));
vi.mock('../../ui/MetricChart.tsx', () => {
  imported.count += 1;
  return { MetricChart: () => <div data-testid="chart" /> };
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

describe('HostChecksCard chart chunk', () => {
  it('is not fetched before anyone asks for a graph', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    // The whole point of the lazy import: a host page nobody graphs on
    // never pays for uPlot.
    expect(await screen.findByRole('button', { name: 'Graph cpu usage_percent' })).toBeInTheDocument();
    expect(imported.count).toBe(0);
  });

  it('starts loading on the click, not on the series that never arrives', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      // A series that fails is the sharpest form of "the data is not
      // here": `<MetricChart>` is never rendered on this path at all, so
      // an import that happened anyway can only have come from the click.
      'GET /api/v1/orgs/acme/hosts/h1/metrics': () =>
        new Response(JSON.stringify({ detail: 'the query timed out' }), {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderCard();

    await userEvent.click(await screen.findByRole('button', { name: 'Graph cpu usage_percent' }));

    expect(await screen.findByRole('alert')).toBeInTheDocument();
    expect(screen.queryByTestId('chart')).not.toBeInTheDocument();
    expect(imported.count).toBe(1);
  });

  it('is fetched once, however often the graph is reopened', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': CHECKS,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/metrics': {
        check: 'cpu', item: '', metric: 'usage_percent', range: '1h', from: NOW, to: NOW,
        points: [{ at: NOW, value: 12.5 }],
      },
    });
    renderCard();

    const graph = await screen.findByRole('button', { name: 'Graph cpu usage_percent' });
    await userEvent.click(graph);
    expect(await screen.findByTestId('chart')).toBeInTheDocument();
    await userEvent.click(graph);
    await userEvent.click(graph);

    // A dynamic import resolves from the module cache after the first
    // call, so priming it on every open costs nothing — but a prefetch
    // that re-ran the module factory would be a bug worth seeing.
    expect(imported.count).toBe(1);
  });
});
