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
    check({ values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: false, state: null, flapping: false }] }),
    check({
      definition_id: 'd2',
      key: 'disk',
      plugin: 'disk',
      interval_seconds: 300,
      source: 'group',
      source_id: 'g1',
      source_name: 'Web tier',
      values: [{ item: '/var', metric: 'used_percent', value: 81.25, at: NOW, stale: false, state: null, flapping: false }],
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
      values: [{ item: 'nginx.service', metric: 'state', label: 'active', at: NOW, stale: false, state: null, flapping: false }],
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
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
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
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
              { item: '', metric: 'usage_percent', error: 'read /proc/stat: permission denied', at: NOW, stale: true, state: null, flapping: false },
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            key: 'nginx',
            plugin: 'service',
            values: [{ item: 'nginx.service', metric: 'state', error: 'dbus: connection refused', at: NOW, stale: true, state: null, flapping: false }],
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
              { item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: true, state: null, flapping: false },
              { item: '', metric: 'load1', value: 0.4, at: '2026-09-27T11:00:00Z', stale: false, state: null, flapping: false },
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

  it('renders each of the four rule-engine states with its own label, plus "not yet evaluated" for a tuple with no state row', async () => {
    // The five markers a colour-blind operator (or a black-and-white
    // screenshot) must be able to tell apart from text alone — colour is
    // never the only carrier (v0.3b-2 spec).
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            values: [
              { item: '', metric: 'a_metric', value: 1, at: NOW, stale: false, state: 'ok', flapping: false },
              { item: '', metric: 'b_metric', value: 1, at: NOW, stale: false, state: 'warn', flapping: false },
              { item: '', metric: 'c_metric', value: 1, at: NOW, stale: false, state: 'crit', flapping: false },
              { item: '', metric: 'd_metric', value: 1, at: NOW, stale: false, state: 'unknown', flapping: false },
              { item: '', metric: 'e_metric', value: 1, at: NOW, stale: false, state: null, flapping: false },
            ],
          }),
        ],
      },
    });
    renderCard();

    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(row('cpu', 'a_metric').getByText('ok')).toBeInTheDocument();
    expect(row('cpu', 'b_metric').getByText('warn')).toBeInTheDocument();
    expect(row('cpu', 'c_metric').getByText('crit')).toBeInTheDocument();
    expect(row('cpu', 'd_metric').getByText('unknown')).toBeInTheDocument();
    // Never rendered as "ok" — a tuple the rule engine has not looked at
    // yet is not a clean bill of health.
    expect(row('cpu', 'e_metric').getByText('not yet evaluated')).toBeInTheDocument();
    expect(row('cpu', 'e_metric').queryByText('ok')).not.toBeInTheDocument();
  });

  it('flags a flapping tuple beside its state, not instead of it', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: false, state: 'warn', flapping: true }],
          }),
        ],
      },
    });
    renderCard();

    expect(await screen.findByText('flapping')).toBeInTheDocument();
    // The flag is additional information, not a replacement for the state
    // it decorates.
    expect(row('cpu', 'usage_percent').getByText('warn')).toBeInTheDocument();
  });

  it('says nothing about flapping for a stable tuple', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: false, state: 'ok', flapping: false }],
          }),
        ],
      },
    });
    renderCard();

    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(screen.queryByText('flapping')).not.toBeInTheDocument();
  });

  // backlog 3.36: a stopped evaluator leaves a verdict exactly where it
  // was while fresh samples keep arriving — `stale` (the reading) and
  // `state_stale` (the verdict) are independent flags, and only the
  // server can compute the second one (it alone knows the check's
  // resolved interval). The marker is a Pill with its own text ("stale
  // verdict"), not a colour change, so it reads in a black-and-white
  // screenshot exactly like `stale` already does.
  it('marks a stale verdict distinctly from a stale reading', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [
          check({
            values: [
              {
                item: '',
                metric: 'usage_percent',
                value: 12.5,
                at: NOW,
                stale: false,
                state: 'crit',
                since: '2026-09-27T10:00:00Z',
                evaluated_at: '2026-09-27T10:40:00Z',
                state_stale: true,
                flapping: false,
              },
              {
                item: '',
                metric: 'load1',
                value: 0.4,
                at: NOW,
                stale: false,
                state: 'ok',
                since: NOW,
                evaluated_at: NOW,
                state_stale: false,
                flapping: false,
              },
            ],
          }),
        ],
      },
    });
    renderCard();

    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    // The reading itself is fresh — no "stale" pill — while the verdict is
    // marked old, proving the two are read from independent flags rather
    // than one driving the other.
    expect(row('cpu', 'usage_percent').queryByText('stale')).not.toBeInTheDocument();
    expect(row('cpu', 'usage_percent').getByText('stale verdict')).toBeInTheDocument();
    expect(row('cpu', 'usage_percent').getByText('crit')).toBeInTheDocument();
    // A tuple whose verdict is current carries no such marker.
    expect(row('cpu', 'load1').queryByText('stale verdict')).not.toBeInTheDocument();
  });

  it('opens the deciding rule for the value on screen from that value’s own row', async () => {
    // The explainability question an operator asks — "which rule decided
    // THIS number" — about the value already in front of them, not from a
    // second list they have to scan (review round 1, "the effective-rules
    // panel's shape").
    mockApi({
      'GET /api/v1/orgs/acme/monitoring-groups': {
        groups: [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 10, created_at: '', updated_at: '' }],
      },
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': {
        effective: [
          {
            check_key: 'cpu',
            item: '',
            metric: 'usage_percent',
            severity: 'warn',
            observed: true,
            winner: {
              id: 'r1', name: 'Host override', enabled: true, origin: 'user', scope: 'host', host_id: 'h1',
              check_key: 'cpu', metric: 'usage_percent', severity: 'warn', mode: 'form',
              form: { metric: 'usage_percent', op: '>=', value: 90 }, source: 'usage_percent >= 90',
              for_seconds: 0, created_at: '', updated_at: '',
            },
            shadowed: [
              {
                id: 'r2', name: 'Web tier warning', enabled: true, origin: 'user', scope: 'group', group_id: 'g1',
                check_key: 'cpu', metric: 'usage_percent', severity: 'warn', mode: 'form',
                form: { metric: 'usage_percent', op: '>=', value: 80 }, source: 'usage_percent >= 80',
                for_seconds: 0, created_at: '', updated_at: '',
              },
            ],
          },
        ],
      },
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
    });
    renderCard();

    const toggle = await screen.findByRole('button', { name: 'Rules for cpu usage_percent' });
    expect(screen.queryByText('Host override')).not.toBeInTheDocument();

    await userEvent.click(toggle);

    expect(await screen.findByText('Host override')).toBeInTheDocument();
    expect(screen.getByText('Host')).toBeInTheDocument();
    expect(screen.getByText(/1 shadowed/)).toBeInTheDocument();
    // A metric with nothing behind it (disk, nginx in THREE_SOURCES) offers
    // no such control at all — nothing to explain.
    expect(screen.queryByRole('button', { name: /Rules for disk/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Rules for nginx/ })).not.toBeInTheDocument();
  });

  it('closes the rule disclosure on a second click, and keeps the value’s own state and graph controls untouched', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': {
        effective: [
          {
            check_key: 'cpu',
            item: '',
            metric: 'usage_percent',
            severity: 'warn',
            observed: true,
            winner: {
              id: 'r1', name: 'Host override', enabled: true, origin: 'user', scope: 'host', host_id: 'h1',
              check_key: 'cpu', metric: 'usage_percent', severity: 'warn', mode: 'form',
              form: { metric: 'usage_percent', op: '>=', value: 90 }, source: 'usage_percent >= 90',
              for_seconds: 0, created_at: '', updated_at: '',
            },
            shadowed: [],
          },
        ],
      },
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
    });
    renderCard();

    const toggle = await screen.findByRole('button', { name: 'Rules for cpu usage_percent' });
    await userEvent.click(toggle);
    expect(await screen.findByText('Host override')).toBeInTheDocument();

    await userEvent.click(toggle);
    expect(screen.queryByText('Host override')).not.toBeInTheDocument();
    // The rest of the row is unaffected by opening or closing this panel.
    expect(screen.getByText('12.5')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Graph cpu usage_percent' })).toBeInTheDocument();
  });

  it('names a skewed host clock rather than leaving every reading amber without a cause', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        // Measured at ingest: this host's clock runs four minutes behind
        // the manager's. Its readings are accepted (the window is an hour
        // wide) and then marked stale for ever (three 60 s intervals),
        // with the agent connected and perfectly healthy.
        clock_offset_seconds: 243.7,
        checks: [check({ values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: true, state: null, flapping: false }] })],
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: false,
        clock_offset_seconds: 243.7,
        checks: [check({ values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: true, state: null, flapping: false }] })],
      },
    });
    renderCard();

    expect(await screen.findByText(/not being monitored/)).toBeInTheDocument();
    expect(screen.queryByText(/clock is about/)).not.toBeInTheDocument();
  });

  it('says nothing about a clock nobody measured, and nothing about a punctual one', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
    });
    const { unmount } = renderCard();
    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    // No measurement in the envelope at all — a manager that has ingested
    // nothing from this host. Absence is not "this clock is fine".
    expect(screen.queryByText(/clock is about/)).not.toBeInTheDocument();
    unmount();

    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': { host_id: 'h1', monitored: true, checks: [] },
    });
    const { unmount } = renderCard();

    expect(await screen.findByText('No checks resolve for this host yet.')).toBeInTheDocument();
    expect(screen.queryByText(/not being monitored/)).not.toBeInTheDocument();
    unmount();

    // `checks` is typed `HostCheckDTO[] | null` by the generated schema —
    // Go's nil slice reaches the SPA as JSON null, not [].
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        monitored: true,
        checks: [check({ values: [{ item: '', metric: 'load1', value: 1 / 3, at: NOW, stale: false, state: null, flapping: false }] })],
      },
    });
    renderCard();

    expect(await screen.findByText('0.33')).toBeInTheDocument();
    expect(screen.queryByText('0.3333333333333333')).not.toBeInTheDocument();
  });

  it('says an unmonitored host is not being watched instead of listing values that look live', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': {
        host_id: 'h1',
        // The effective set does not depend on the flag: these checks WOULD
        // apply, but nothing runs them.
        monitored: false,
        checks: [check({ values: [{ item: '', metric: 'usage_percent', value: 12.5, at: NOW, stale: false, state: null, flapping: false }] })],
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
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
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    const { unmount } = renderCard(true, false);
    expect(await screen.findByRole('heading', { name: 'cpu' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Manage checks' })).not.toBeInTheDocument();
    unmount();

    renderCard(true, true);
    expect(await screen.findByRole('link', { name: 'Manage checks' })).toBeInTheDocument();
  });

  it('asks for nothing at all without checks.read', async () => {
    const fetch = mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard(false);

    expect(await screen.findByText(/Monitoring is visible to members/)).toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('reports a failed read in the card instead of an empty monitoring section', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/checks': () => new Response(JSON.stringify({ detail: 'checks unavailable' }), { status: 500 }),
    });
    renderCard();

    expect(await screen.findByRole('alert')).toHaveTextContent('checks unavailable');
  });

  describe('item decisions (v0.3b-3)', () => {
    /** An ignored value as the server actually sends it for a tuple with
     * no prior history: `state: null`, because nothing has judged it. */
    const ignoredValue = (over: Record<string, unknown> = {}) => ({
      item: '/var',
      metric: 'used_percent',
      value: 81.25,
      at: NOW,
      stale: false,
      state: null,
      flapping: false,
      ignored: true,
      ...over,
    });

    const diskCheck = (values: unknown[]) =>
      check({ definition_id: 'd2', key: 'disk', plugin: 'disk', interval_seconds: 300, source: 'org', values });

    it('renders an ignored row as text — never "not yet evaluated" — even when a stale crit state is still attached', async () => {
      // Review focus: internal/items' own Apply deletes the state row the
      // instant an item is ignored, so the next read is guaranteed to
      // carry state: null — but this fixture goes further and keeps
      // state: 'crit' on the wire, to prove `ignored` wins regardless of
      // whatever the state field happens to say, not only in the one
      // shape the server actually produces.
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
        'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
        'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
        'GET /api/v1/orgs/acme/hosts/h1/checks': {
          host_id: 'h1',
          monitored: true,
          checks: [diskCheck([ignoredValue({ state: 'crit' })])],
        },
      });
      renderCard();

      expect(await screen.findByText('ignored')).toBeInTheDocument();
      expect(row('disk', '/var used_percent').queryByText('crit')).not.toBeInTheDocument();
      expect(row('disk', '/var used_percent').queryByText('not yet evaluated')).not.toBeInTheDocument();
    });

    it('names an organization pattern as the origin, with its note, once the row is opened — not before', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/items': {
          items: [
            {
              check_key: 'disk',
              item: '/var',
              first_seen: NOW,
              last_seen: NOW,
              reported: true,
              decision: 'ignore',
              decided_by: {
                id: 'od1',
                scope: 'org',
                check_key: 'disk',
                match_kind: 'glob',
                pattern: '/mnt/scratch-*',
                decision: 'ignore',
                note: 'Backup volume runs hot on purpose.',
                created_at: NOW,
                updated_at: NOW,
              },
            },
          ],
        },
        'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
        'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
        'GET /api/v1/orgs/acme/hosts/h1/checks': { host_id: 'h1', monitored: true, checks: [diskCheck([ignoredValue()])] },
      });
      renderCard();

      expect(await screen.findByText('ignored')).toBeInTheDocument();
      // Closed, not absent: a native <details>, so the content is already
      // in the DOM for a click handler to reveal with no extra fetch —
      // what "not before" asserts is that it starts unopened.
      expect(screen.getByText(/organization pattern/)).not.toBeVisible();

      await user.click(screen.getByText('Why ignored'));

      expect(
        screen.getByText('By the organization pattern `/mnt/scratch-*` — Backup volume runs hot on purpose.'),
      ).toBeInTheDocument();
    });

    it('names "set on this host" for a decision made here, without repeating the pattern', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/items': {
          items: [
            {
              check_key: 'disk',
              item: '/var',
              first_seen: NOW,
              last_seen: NOW,
              reported: true,
              decision: 'ignore',
              decided_by: {
                id: 'hd1',
                scope: 'host',
                host_id: 'h1',
                check_key: 'disk',
                match_kind: 'exact',
                pattern: '/var',
                decision: 'ignore',
                created_at: NOW,
                updated_at: NOW,
              },
            },
          ],
        },
        'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
        'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
        'GET /api/v1/orgs/acme/hosts/h1/checks': { host_id: 'h1', monitored: true, checks: [diskCheck([ignoredValue()])] },
      });
      renderCard();

      await user.click(await screen.findByText('Why ignored'));

      expect(screen.getByText('Set on this host')).toBeInTheDocument();
      expect(screen.queryByText(/organization pattern/)).not.toBeInTheDocument();
    });

    // Review focus 3: an organization pattern would ignore this item, but a
    // host-scoped exact watch overrules it. The row must show the ordinary
    // verdict (not "ignored") AND name the host decision as the one that
    // decided it — a resolver that silently ignored the org pattern
    // altogether would ALSO show the right verdict, so the verdict alone
    // proves nothing; only the disclosure naming the host decision does.
    it('shows a host watch overruling an organization pattern, and names the host decision as the one that decided it', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/items': {
          items: [
            {
              check_key: 'disk',
              item: '/var',
              first_seen: NOW,
              last_seen: NOW,
              reported: true,
              decision: 'watch',
              decided_by: {
                id: 'hd1',
                scope: 'host',
                host_id: 'h1',
                check_key: 'disk',
                match_kind: 'exact',
                pattern: '/var',
                decision: 'watch',
                note: 'Needs eyes on it despite the fleet-wide pattern.',
                created_at: NOW,
                updated_at: NOW,
              },
            },
          ],
        },
        'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
        'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
        'GET /api/v1/orgs/acme/hosts/h1/checks': {
          host_id: 'h1',
          monitored: true,
          checks: [
            diskCheck([
              { item: '/var', metric: 'used_percent', value: 81.25, at: NOW, stale: false, state: 'ok', flapping: false, ignored: false },
            ]),
          ],
        },
      });
      renderCard();

      expect(await screen.findByText('ok')).toBeInTheDocument();
      expect(screen.queryByText('ignored')).not.toBeInTheDocument();

      await user.click(screen.getByText('Why watched'));

      expect(
        screen.getByText('Set on this host — Needs eyes on it despite the fleet-wide pattern.'),
      ).toBeInTheDocument();
    });

    it('offers the row action to ignore an item, and posts a host-scoped exact decision for it', async () => {
      const user = userEvent.setup();
      let posted: unknown;
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
        'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
        'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
        'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
        'POST /api/v1/orgs/acme/item-decisions': (req: MockRequest) => {
          posted = req.body;
          return { decision: { id: 'new', ...(req.body as object), created_at: NOW, updated_at: NOW } };
        },
      });
      renderCard();

      await user.click(await screen.findByRole('button', { name: 'Ignore disk /var' }));

      expect(posted).toEqual({
        scope: 'host',
        host_id: 'h1',
        check_key: 'disk',
        match_kind: 'exact',
        pattern: '/var',
        decision: 'ignore',
      });
    });

    it('offers no item decision action for a whole-host metric — an item with no name is not a thing to ignore', async () => {
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
        'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
        'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
        'GET /api/v1/orgs/acme/hosts/h1/checks': THREE_SOURCES,
      });
      renderCard();

      await screen.findByRole('heading', { name: 'cpu' });
      expect(screen.queryByRole('button', { name: /Ignore cpu/ })).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /Watch cpu/ })).not.toBeInTheDocument();
    });

    it('hides the row action from a viewer, but still shows the ignored marker and its origin', async () => {
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/items': {
          items: [
            {
              check_key: 'disk',
              item: '/var',
              first_seen: NOW,
              last_seen: NOW,
              reported: true,
              decision: 'ignore',
              decided_by: {
                id: 'hd1',
                scope: 'host',
                host_id: 'h1',
                check_key: 'disk',
                match_kind: 'exact',
                pattern: '/var',
                decision: 'ignore',
                created_at: NOW,
                updated_at: NOW,
              },
            },
          ],
        },
        'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
        'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
        'GET /api/v1/orgs/acme/hosts/h1/checks': { host_id: 'h1', monitored: true, checks: [diskCheck([ignoredValue()])] },
      });
      renderCard(true, false);

      expect(await screen.findByText('ignored')).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /Watch disk/ })).not.toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /Ignore disk/ })).not.toBeInTheDocument();
    });
  });
});
