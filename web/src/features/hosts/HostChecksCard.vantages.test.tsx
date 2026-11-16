import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { HostChecksCard } from './HostChecksCard.tsx';

const AT = new Date(Date.now() - 60_000).toISOString();
const AGENT = '0190a000-0000-7000-8000-00000000000a';

/** A tcp check probed from the Pulsix server and one agent, and a cpu
 * check without probing points. */
const CHECKS = {
  host_id: 'h1',
  monitored: true,
  checks: [
    {
      definition_id: 'd1',
      key: 'site',
      plugin: 'tcp',
      params: { port: 443 },
      interval_seconds: 60,
      enabled: true,
      origin: 'custom',
      source: 'host',
      values: [{ item: '', metric: 'tcp.connect_ms', value: 12, at: AT, stale: false, state: null, flapping: false, state_stale: false, ignored: false }],
      fail_policy: 'majority',
      vantages: [
        { vantage: 'manager', name: '', kind: 'manager', status: 'up', at: AT, observations: [{ item: '', metric: 'tcp.up', value: 1 }, { item: '', metric: 'tcp.connect_ms', value: 12 }] },
        { vantage: AGENT, name: 'edge-1', kind: 'agent', status: 'down', at: AT, observations: [{ item: '', metric: 'tcp.up', value: 0 }, { item: '', metric: 'tcp.connect_ms', value: 40.5 }] },
      ],
    },
    {
      definition_id: 'd2',
      key: 'cpu',
      plugin: 'cpu',
      params: {},
      interval_seconds: 60,
      enabled: true,
      origin: 'seed',
      source: 'org',
      values: [{ item: '', metric: 'usage_percent', value: 3, at: AT, stale: false, state: null, flapping: false, state_stale: false, ignored: false }],
    },
  ],
};

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    component: () => <HostChecksCard org="acme" hostId="h1" canRead canWrite={false} />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <ConfirmProvider>
          <RouterProvider router={router} />
        </ConfirmProvider>
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const block = (key: string) => within(screen.getByRole('heading', { name: key }).closest('li') as HTMLElement);

function routes(extra: Record<string, unknown> = {}) {
  return {
    'GET /api/v1/orgs/acme/hosts/h1/checks': CHECKS,
    'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
    'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    ...extra,
  };
}

describe('HostChecksCard — probing points', () => {
  it('lists the probing points of a check under it, and none under a check without', async () => {
    mockApi(routes());
    renderCard();
    const table = await screen.findByRole('table', { name: 'Probing points of site' });
    const rows = within(table).getAllByRole('row').slice(1).map((r) => within(r).getAllByRole('cell').map((c) => c.textContent));
    expect(rows).toEqual([
      ['Pulsix server', 'up', '12 ms', '1 minute ago'],
      ['edge-1', 'down', '40.5 ms', '1 minute ago'],
    ]);
    expect(block('site').getByText('Down when most probing points see it down.')).toBeInTheDocument();
    expect(block('cpu').queryByRole('table')).not.toBeInTheDocument();
  });

  it('says so when a check has no probing point yet', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/hosts/h1/checks': { ...CHECKS, checks: [{ ...CHECKS.checks[0], vantages: [] }] } }));
    renderCard();
    expect(await screen.findByText('No probing point is assigned yet.')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it("switches a check's graph between the combined series and one probing point", async () => {
    const user = userEvent.setup();
    const asked: Array<Record<string, string>> = [];
    mockApi(
      routes({
        'GET /api/v1/orgs/acme/hosts/h1/metrics': (req: MockRequest) => {
          const q = Object.fromEntries(req.search.entries());
          asked.push(q);
          return { check: q.check, item: '', metric: q.metric, range: q.range, from: AT, to: AT, points: [] };
        },
      }),
    );
    renderCard();
    await user.click(await screen.findByRole('button', { name: 'Graph site tcp.connect_ms' }));
    await waitFor(() => expect(asked).toHaveLength(1));
    expect(asked[0]).toEqual({ check: 'site', metric: 'tcp.connect_ms', range: '1h' });
    expect(screen.getByRole('combobox', { name: 'Probing point' })).toHaveValue('Combined');

    await pickOption(screen, 'Probing point', 'edge-1');
    await waitFor(() => expect(asked).toHaveLength(2));
    expect(asked[1]).toEqual({ check: 'site', metric: 'tcp.connect_ms', range: '1h', vantage: AGENT });

    await user.click(screen.getByRole('button', { name: '24 h' }));
    await waitFor(() => expect(asked).toHaveLength(3));
    expect(asked[2]).toEqual({ check: 'site', metric: 'tcp.connect_ms', range: '24h', vantage: AGENT });

    await pickOption(screen, 'Probing point', 'Combined');
    await waitFor(() => expect(asked.at(-1)).toEqual({ check: 'site', metric: 'tcp.connect_ms', range: '24h' }));
  });

  it('offers no probing point choice on the graph of a check without', async () => {
    const user = userEvent.setup();
    mockApi(
      routes({
        'GET /api/v1/orgs/acme/hosts/h1/metrics': { check: 'cpu', item: '', metric: 'usage_percent', range: '1h', from: AT, to: AT, points: [] },
      }),
    );
    renderCard();
    await user.click(await screen.findByRole('button', { name: 'Graph cpu usage_percent' }));
    expect(await screen.findByRole('button', { name: '1 h' })).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: 'Probing point' })).not.toBeInTheDocument();
  });
});
