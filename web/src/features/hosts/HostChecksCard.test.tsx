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

/**
 * `retry` is the QueryClient default the card's own hooks have to override
 * to matter. Left at false — the value every other test here wants — a
 * missing `retry: false` on useHostMetric would be invisible, because the
 * client would already be doing it. One test raises it on purpose.
 */
function renderCard(canRead = true, canWrite = true, retry: number | boolean = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry } } });
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

  it('renders a collection error as the failure, and keeps the graph to the history behind it', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            values: [
              // The shape the server can actually produce: an error row
              // carries neither value nor label (migration 00044's CHECK
              // on check_current, and UpsertCheckCurrent writing
              // EXCLUDED.value wholesale). The previous fixture put 91.2
              // beside the error and asserted it was not rendered — an
              // assertion that could never fail, about a row the database
              // forbids.
              { item: '', metric: 'usage_percent', error: 'read /proc/stat: permission denied', at: NOW, stale: true },
            ],
          }),
        ],
      },
    });
    renderCard();

    expect(await screen.findByText('collection failed')).toBeInTheDocument();
    expect(row('cpu', 'usage_percent').getByText('read /proc/stat: permission denied')).toBeInTheDocument();
    // Nothing numeric is invented in place of the missing reading.
    expect(row('cpu', 'usage_percent').queryByText('—')).not.toBeInTheDocument();
    // And the button that reaches the samples survives the incident: GET
    // /hosts/{id}/metrics answers this tuple with its points while the
    // latest collection is failing, which is precisely when an operator
    // wants to see the line into the failure.
    expect(screen.getByRole('button', { name: 'Graph cpu usage_percent' })).toBeInTheDocument();
  });

  it('offers no graph for a discrete check whose collection is failing either', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            key: 'nginx',
            plugin: 'service',
            values: [{ item: 'nginx.service', metric: 'state', error: 'dbus: connection refused', at: NOW, stale: true }],
          }),
        ],
      },
    });
    renderCard();

    // An error row looks the same whatever produced it, so the plugin is
    // what decides: a service check has labels behind it, never a series,
    // and a graph opened on one has nothing to draw.
    expect(await screen.findByText('collection failed')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Graph/ })).not.toBeInTheDocument();
  });

  it('marks exactly what the server calls stale, never what the timestamps suggest', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            // Deliberately the inverse of what any client-side rule would
            // conclude. On a 60 s check, "older than three intervals"
            // arithmetic over these two readings says load1 (an hour old)
            // is stale and usage_percent (the newest) is not — the exact
            // opposite of the flags below. Only code that renders the
            // server's answer can pass both assertions; a recomputation
            // fails them both, which the previous fixture (whose
            // timestamps happened to agree with the arithmetic) could not
            // detect.
            values: [
              { item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: true },
              { item: '', metric: 'load1', value: 0.4, at: '2026-09-27T11:00:00Z', stale: false },
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

  it('names a skewed host clock rather than leaving every reading amber without a cause', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        // Measured at ingest: this host's clock runs four minutes behind
        // the manager's. Its readings are accepted (the window is an hour
        // wide) and then marked stale for ever (three 60 s intervals),
        // with the agent connected and perfectly healthy.
        clock_offset_seconds: 243.7,
        checks: [check({ values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: true }] })],
      },
    });
    renderCard();

    expect(await screen.findByText(/clock is about 4 min behind/)).toBeInTheDocument();
    // The stale badge stays: the server's verdict is not overridden, only
    // explained.
    expect(row('cpu', 'usage_percent').getByText('stale')).toBeInTheDocument();
  });

  it('says nothing about a clock on a host nobody is monitoring', async () => {
    // With nothing being measured there are no readings on the card for a
    // clock to have skewed, so the note is gated on `monitored` — and the
    // gate was held by nothing: deleting it passed every test here,
    // because every other envelope in this file is monitored. The offset
    // is the same four minutes the monitored case renders.
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: false,
        clock_offset_seconds: 243.7,
        checks: [check({ values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: true }] })],
      },
    });
    renderCard();

    expect(await screen.findByText(/not being monitored/)).toBeInTheDocument();
    expect(screen.queryByText(/clock is about/)).not.toBeInTheDocument();
  });

  it('says nothing about a clock nobody measured, and nothing about a punctual one', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
    });
    const { unmount } = renderCard();
    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    // No measurement in the envelope at all — a manager that has ingested
    // nothing from this host. Absence is not "this clock is fine".
    expect(screen.queryByText(/clock is about/)).not.toBeInTheDocument();
    unmount();

    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': { ...THREE_SOURCES, clock_offset_seconds: 0.8 },
    });
    renderCard();
    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(screen.queryByText(/clock is about/)).not.toBeInTheDocument();
  });

  it('distinguishes a host with nothing configured from one nobody is watching', async () => {
    // Three states share one card, and the empty list is the one that reads
    // like a bug when it is silent.
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': { host_id: 'h1', monitored: true, checks: [] },
    });
    const { unmount } = renderCard();

    expect(await screen.findByText('No checks resolve for this host yet.')).toBeInTheDocument();
    expect(screen.queryByText(/not being monitored/)).not.toBeInTheDocument();
    unmount();

    // `checks` is typed `HostCheckDTO[] | null` by the generated schema —
    // Go's nil slice reaches the SPA as JSON null, not [].
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': { host_id: 'h1', monitored: false, checks: null },
    });
    renderCard();

    expect(await screen.findByText(/This host is not being monitored/)).toBeInTheDocument();
    expect(screen.getByText('No checks resolve for this host yet.')).toBeInTheDocument();
  });

  it('lists a check the winning scope turned off, marked as off', async () => {
    // "false means the winning scope turned this check off; it is listed so
    // the GUI can show where" — dropping the row instead would hide the
    // override that did it.
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [check({ enabled: false, source: 'group', source_name: 'Web tier', values: [] })],
      },
    });
    renderCard();

    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(block('cpu').getByText('disabled')).toBeInTheDocument();
    expect(block('cpu').getByText('Web tier')).toBeInTheDocument();
    expect(block('cpu').getByText('No reading yet.')).toBeInTheDocument();
  });

  it('renders a load average at two decimals rather than the float it arrived as', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [check({ values: [{ item: '', metric: 'load1', value: 1 / 3, at: NOW, stale: false }] })],
      },
    });
    renderCard();

    expect(await screen.findByText('0.33')).toBeInTheDocument();
    expect(screen.queryByText('0.3333333333333333')).not.toBeInTheDocument();
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

  it('shows the server’s own message once, without retrying a final answer', async () => {
    const user = userEvent.setup();
    let attempts = 0;
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/metrics': () => {
        attempts += 1;
        return new Response(JSON.stringify({ detail: 'host has no metric "used_percent" for check "disk"' }), { status: 404 });
      },
    });
    // A client that WOULD retry three times, so the hook's own retry: false
    // is what decides the count. With the suite's usual retry: false client
    // this assertion would hold no matter what the hook asked for.
    renderCard(true, true, 3);

    await user.click(await screen.findByRole('button', { name: 'Graph disk /var used_percent' }));

    expect(await screen.findByText('host has no metric "used_percent" for check "disk"')).toBeInTheDocument();
    // 404 for an unknown tuple, 400 for a discrete metric or a bad range:
    // every failure this route produces is final, and asking again only
    // delays the message.
    expect(attempts).toBe(1);
  });

  it('shows which window is on screen', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/metrics': { check: 'cpu', item: '', metric: 'usage_percent', range: '1h', from: NOW, to: NOW, points: [] },
    });
    renderCard();

    await user.click(await screen.findByRole('button', { name: 'Graph cpu usage_percent' }));

    // Without this the range switch is three identical buttons and an
    // operator has no way to tell which window they are looking at.
    expect(screen.getByRole('button', { name: '1 h', pressed: true })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '24 h', pressed: false })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '7 d', pressed: false })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: '24 h' }));

    expect(screen.getByRole('button', { name: '24 h', pressed: true })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '1 h', pressed: false })).toBeInTheDocument();
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
