import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { HostChecksCard } from './HostChecksCard.tsx';

const NOW = '2026-09-27T12:00:00Z';

const check = (over: Record<string, unknown> = {}) => ({
  definition_id: 'd1',
  key: 'cpu',
  plugin: 'cpu',
  params: {},
  interval_seconds: 60,
  enabled: true,
  origin: 'seed',
  source: 'org',
  values: [],
  ...over,
});

/** The three sources a check can win at, one each, with one value apiece:
 * a whole-host numeric metric, a per-item numeric metric, and a discrete
 * one carried as a label. */
const THREE_SOURCES = {
  host_id: 'h1',
  monitored: true,
  checks: [
    check({ values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: false }] }),
    check({
      definition_id: 'd2',
      key: 'disk',
      plugin: 'disk',
      interval_seconds: 300,
      source: 'group',
      source_id: 'g1',
      source_name: 'Web tier',
      values: [{ item: '/var', metric: 'used_percent', value: 81.25, at: NOW, stale: false }],
    }),
    check({
      definition_id: 'd3',
      key: 'nginx',
      plugin: 'service',
      interval_seconds: 30,
      origin: 'custom',
      source: 'host',
      source_id: 'h1',
      source_name: 'web-1',
      values: [{ item: 'nginx.service', metric: 'state', label: 'active', at: NOW, stale: false }],
    }),
  ],
};

function renderCard(canRead = true, canWrite = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks', component: () => <div>CHECKS PAGE</div> });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    component: () => <HostChecksCard org="acme" hostId="h1" canRead={canRead} canWrite={canWrite} />,
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

/** The list item for one check, anchored on its own heading so a metric
 * name that repeats across checks cannot be matched in the wrong block. */
const block = (key: string) => within(screen.getByRole('heading', { name: key }).closest('li') as HTMLElement);

/** The list item for one metric inside a check's block. */
const row = (key: string, metric: string) => within(block(key).getByText(metric).closest('li') as HTMLElement);

describe('HostChecksCard', () => {
  it('renders each check with the scope it is inherited from and its current values', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES });
    renderCard();

    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();

    // The source is rendered as the scope itself — never as the raw id the
    // envelope carries.
    expect(block('cpu').getByText('Organization')).toBeInTheDocument();
    expect(block('disk').getByText('Web tier')).toBeInTheDocument();
    expect(block('nginx').getByText('This host')).toBeInTheDocument();

    // Each check's own interval, not a shared default.
    expect(block('cpu').getByText('every 1 min')).toBeInTheDocument();
    expect(block('disk').getByText('every 5 min')).toBeInTheDocument();
    expect(block('nginx').getByText('every 30 s')).toBeInTheDocument();

    // Current values: two numeric, one discrete.
    expect(row('cpu', 'usage_percent').getByText('12.5')).toBeInTheDocument();
    expect(row('disk', '/var used_percent').getByText('81.25')).toBeInTheDocument();
    expect(row('nginx', 'nginx.service state').getByText('active')).toBeInTheDocument();
  });

  it('offers a graph for numeric metrics only', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES });
    renderCard();

    expect(await screen.findByRole('button', { name: 'Graph cpu usage_percent' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Graph disk /var used_percent' })).toBeInTheDocument();
    // A discrete metric has no series at all: GET /hosts/{id}/metrics
    // refuses it with a 400, so the card must not offer a control whose
    // only possible outcome is that error.
    expect(screen.queryByRole('button', { name: /Graph nginx/ })).not.toBeInTheDocument();
  });

  it('renders a collection error as a failure, not as a stale-looking number', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            values: [
              {
                item: '',
                metric: 'usage_percent',
                // The envelope still carries the last known reading beside
                // the error. Showing 91.2 here would read as a current
                // measurement of a host nobody is successfully measuring.
                value: 91.2,
                error: 'read /proc/stat: permission denied',
                at: NOW,
                stale: true,
              },
            ],
          }),
        ],
      },
    });
    renderCard();

    expect(await screen.findByText('collection failed')).toBeInTheDocument();
    expect(row('cpu', 'usage_percent').getByText('read /proc/stat: permission denied')).toBeInTheDocument();
    expect(screen.queryByText('91.2')).not.toBeInTheDocument();
  });

  it('marks a stale entry and leaves a fresh sibling alone', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            values: [
              // `stale` is the server's answer (three of the check's own
              // intervals without a reading). The card renders the flag; it
              // does not recompute the rule.
              { item: '', metric: 'usage_percent', value: 12.5, at: '2026-09-27T11:00:00Z', stale: true },
              { item: '', metric: 'load1', value: 0.4, at: NOW, stale: false },
            ],
          }),
        ],
      },
    });
    renderCard();

    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(row('cpu', 'usage_percent').getByText('stale')).toBeInTheDocument();
    // A stale reading is still the last thing the host said, so it stays on
    // screen — marked, not hidden.
    expect(row('cpu', 'usage_percent').getByText('12.5')).toBeInTheDocument();
    expect(row('cpu', 'load1').queryByText('stale')).not.toBeInTheDocument();
  });

  it('says an unmonitored host is not being watched instead of listing values that look live', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        // The effective set does not depend on the flag: these checks WOULD
        // apply, but nothing runs them.
        monitored: false,
        checks: [check({ values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: false }] })],
      },
    });
    renderCard();

    expect(await screen.findByText(/This host is not being monitored/)).toBeInTheDocument();
    // The set is still shown, as the set that would apply — but with no
    // reading and no graph, because neither would mean anything.
    expect(screen.getByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(screen.queryByText('12.5')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Graph/ })).not.toBeInTheDocument();
  });

  it('asks for a 1 h window first and re-reads the series when the range changes', async () => {
    const user = userEvent.setup();
    const asked: Array<Record<string, string>> = [];
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/metrics': (req: MockRequest) => {
        const q = Object.fromEntries(req.search.entries());
        asked.push(q);
        return {
          check: q.check,
          item: q.item ?? '',
          metric: q.metric,
          range: q.range,
          from: NOW,
          to: NOW,
          points: [
            { at: '2026-09-27T11:58:00Z', value: 10 },
            { at: '2026-09-27T11:59:00Z', value: 11 },
            { at: NOW, value: 12 },
          ],
        };
      },
    });
    renderCard();

    await user.click(await screen.findByRole('button', { name: 'Graph disk /var used_percent' }));

    await waitFor(() => expect(asked).toHaveLength(1));
    expect(asked[0]).toEqual({ check: 'disk', item: '/var', metric: 'used_percent', range: '1h' });
    expect(await screen.findByRole('img', { name: 'disk /var used_percent over the last hour, 3 samples' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '7 d' }));

    await waitFor(() => expect(asked).toHaveLength(2));
    expect(asked[1]).toEqual({ check: 'disk', item: '/var', metric: 'used_percent', range: '7d' });
    expect(await screen.findByRole('img', { name: /over the last 7 days, 3 samples/ })).toBeInTheDocument();
  });

  it('omits the item parameter for a whole-host metric', async () => {
    const user = userEvent.setup();
    const asked: Array<Record<string, string>> = [];
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/metrics': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { check: 'cpu', item: '', metric: 'usage_percent', range: '1h', from: NOW, to: NOW, points: [] };
      },
    });
    renderCard();

    await user.click(await screen.findByRole('button', { name: 'Graph cpu usage_percent' }));

    await waitFor(() => expect(asked).toHaveLength(1));
    // "omit for a whole-host metric" — not `item=`, which would be a
    // different query string for the same question.
    expect(asked[0]).toEqual({ check: 'cpu', metric: 'usage_percent', range: '1h' });
    expect(await screen.findByText('No samples in this range.')).toBeInTheDocument();
  });

  it('shows the server’s own message when the series cannot be read', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/metrics': () =>
        new Response(JSON.stringify({ detail: 'host has no metric "used_percent" for check "disk"' }), { status: 404 }),
    });
    renderCard();

    await user.click(await screen.findByRole('button', { name: 'Graph disk /var used_percent' }));

    expect(await screen.findByText('host has no metric "used_percent" for check "disk"')).toBeInTheDocument();
  });

  it('shows a viewer the list and an operator the way to change it', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES });
    const { unmount } = renderCard(true, false);
    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Manage checks' })).not.toBeInTheDocument();
    unmount();

    renderCard(true, true);
    expect(await screen.findByRole('link', { name: 'Manage checks' })).toBeInTheDocument();
  });

  it('asks for nothing at all without checks.read', async () => {
    const fetch = mockApi({ 'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES });
    renderCard(false);

    expect(await screen.findByText(/Monitoring is visible to members/)).toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('reports a failed read in the card instead of an empty monitoring section', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': () => new Response(JSON.stringify({ detail: 'checks unavailable' }), { status: 500 }),
    });
    renderCard();

    expect(await screen.findByRole('alert')).toHaveTextContent('checks unavailable');
  });
});
