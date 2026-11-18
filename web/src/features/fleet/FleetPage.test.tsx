import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { FleetPage } from './FleetPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

const ZONES = { zones: [{ id: 'z1', name: 'Frankfurt', slug: 'fra' }, { id: 'z2', name: 'Amsterdam', slug: 'ams' }] };
const GROUPS = { groups: [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 0, created_at: '', updated_at: '' }] };

const host = (over: Record<string, unknown> = {}) => ({
  host_id: 'h1',
  name: 'web-1',
  zone_id: 'z1',
  state: 'ok',
  crit: 0,
  warn: 0,
  unknown: 0,
  all_ignored: false,
  ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const fleetRoute = createRoute({ getParentRoute: () => orgRoute, path: '/fleet', component: FleetPage });
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([fleetRoute, hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/fleet'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('FleetPage', () => {
  it('lists hosts in the order the API returns them — worst state first — with per-host counts', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/fleet': {
        hosts: [
          host({ host_id: 'h-crit', name: 'db-1', state: 'crit', crit: 2, warn: 1, unknown: 0 }),
          host({ host_id: 'h-warn', name: 'web-1', state: 'warn', crit: 0, warn: 1, unknown: 0 }),
          host({ host_id: 'h-unknown', name: 'cache-1', state: 'unknown', crit: 0, warn: 0, unknown: 3 }),
          host({ host_id: 'h-ok', name: 'lb-1', state: 'ok' }),
          host({ host_id: 'h-nodata', name: 'new-1', state: null }),
          host({ host_id: 'h-ignored', name: 'ignored-1', state: null, all_ignored: true }),
        ],
        totals: { crit: 1, warn: 1, unknown: 1, ok: 1, no_data: 1, all_ignored: 1 },
      },
    });
    renderPage();

    await screen.findByRole('link', { name: 'db-1' });
    // Rendered in the exact server order — this page must not silently
    // re-sort behind the API's own worst-first contract.
    const links = screen.getAllByRole('link', { name: /db-1|web-1|cache-1|lb-1|new-1|ignored-1/ });
    expect(links).toEqual(['db-1', 'web-1', 'cache-1', 'lb-1', 'new-1', 'ignored-1'].map((name) => screen.getByRole('link', { name })));

    expect(screen.getByText('crit')).toBeInTheDocument();
    expect(screen.getByText('warn')).toBeInTheDocument();
    expect(screen.getAllByText('unknown').length).toBeGreaterThan(0);
    expect(screen.getByText('ok')).toBeInTheDocument();
    // A host with no roll-up row at all reads as "no data" — never as a
    // colour it happened to hold before its last check was removed.
    expect(screen.getByText('no data')).toBeInTheDocument();
    // backlog 3.44: a host with no roll-up row because every item it
    // discovered is ignored reads as its own word, not "no data".
    expect(screen.getByText('all ignored')).toBeInTheDocument();

    // The cells sit beside the row's link: its row scopes the assertion to
    // the one host.
    const critRow = within(screen.getByRole('link', { name: 'db-1' }).closest('[data-list-row]') as HTMLElement);
    expect(critRow.getByText('2')).toBeInTheDocument();
  });

  it('makes each host a list row that opens the host, with its one link', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/fleet': {
        hosts: [host({ host_id: 'h-web', name: 'web-01', state: 'warn', warn: 1 }), host({ host_id: 'h-db', name: 'db-01' })],
        totals: { crit: 0, warn: 1, unknown: 0, ok: 1, no_data: 0, all_ignored: 0 },
      },
    });
    const { container } = renderPage();

    const link = await screen.findByRole('link', { name: 'web-01' });
    expect(link).toHaveAttribute('href', '/o/acme/hosts/h-web');
    const rows = container.querySelectorAll('[data-list-row]');
    expect(rows).toHaveLength(2);
    rows.forEach((row) => expect(row.querySelectorAll('a')).toHaveLength(1));
  });

  it('states the totals for the filtered set, not the whole organization', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/fleet': {
        hosts: [host()],
        totals: { crit: 3, warn: 2, unknown: 1, ok: 5, no_data: 4, all_ignored: 2 },
      },
    });
    renderPage();

    const totals = await screen.findByText(/not the whole organization/);
    expect(totals).toHaveTextContent('3 crit');
    expect(totals).toHaveTextContent('2 warn');
    expect(totals).toHaveTextContent('1 unknown');
    expect(totals).toHaveTextContent('5 ok');
    expect(totals).toHaveTextContent('4 no data');
    expect(totals).toHaveTextContent('2 all ignored');
  });

  it('renders an explicit empty state for an organization with no hosts, rather than a blank page', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/zones': { zones: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/fleet': { hosts: [], totals: { crit: 0, warn: 0, unknown: 0, ok: 0, no_data: 0, all_ignored: 0 } },
    });
    renderPage();

    expect(await screen.findByText(/No hosts/)).toBeInTheDocument();
  });

  it('applies the zone filter to the fleet request', async () => {
    const asked: Array<Record<string, string>> = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/fleet': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { hosts: [host()], totals: { crit: 0, warn: 0, unknown: 0, ok: 1, no_data: 0, all_ignored: 0 } };
      },
    });
    renderPage();

    await screen.findByRole('link', { name: 'web-1' });
    await pickOption(screen, 'Filter by zone', { value: 'z2' });

    await waitFor(() => expect(asked.at(-1)?.zone).toBe('z2'));
  });

  it('applies the monitoring group filter to the fleet request', async () => {
    const asked: Array<Record<string, string>> = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/fleet': (req: MockRequest) => {
        asked.push(Object.fromEntries(req.search.entries()));
        return { hosts: [host()], totals: { crit: 0, warn: 0, unknown: 0, ok: 1, no_data: 0, all_ignored: 0 } };
      },
    });
    renderPage();

    await screen.findByRole('link', { name: 'web-1' });
    await pickOption(screen, 'Filter by monitoring group', { value: 'g1' });

    await waitFor(() => expect(asked.at(-1)?.group).toBe('g1'));
  });

  it('applies the tag filter, and surfaces the API’s own refusal of an unsupported expression', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/fleet': (req: MockRequest) => {
        const tag = req.search.get('tag');
        if (tag && tag.includes('!=')) {
          return new Response(
            JSON.stringify({ detail: `tag "${tag}" must be a single "key" or "key=value" token` }),
            { status: 422, headers: { 'Content-Type': 'application/json' } },
          );
        }
        return { hosts: [host()], totals: { crit: 0, warn: 0, unknown: 0, ok: 1, no_data: 0, all_ignored: 0 } };
      },
    });
    renderPage();

    await screen.findByRole('link', { name: 'web-1' });
    const input = screen.getByLabelText('Filter by tag');
    // fireEvent-equivalent single change through user-event's paste, so a
    // component with no internal state of its own (FleetFilters) still
    // reports the full value in one onChange, exactly as typing a pasted
    // value would.
    await user.click(input);
    await user.paste('role!=db');

    expect(await screen.findByText(/must be a single "key" or "key=value" token/)).toBeInTheDocument();
  });

  it('asks for nothing without checks.read', async () => {
    let fleetAsked = false;
    mockApi({
      'GET /api/v1/orgs/acme': org('none'),
      'GET /api/v1/orgs/acme/fleet': () => {
        fleetAsked = true;
        return { hosts: [], totals: { crit: 0, warn: 0, unknown: 0, ok: 0, no_data: 0, all_ignored: 0 } };
      },
    });
    renderPage();

    expect(await screen.findByText(/visible to members/)).toBeInTheDocument();
    expect(fleetAsked).toBe(false);
  });
  // Backlog 3.57: a client whose agent is offline is frozen, so its roll-up
  // is not current. The state cell says "Off" in the neutral tone, the last
  // state stays readable but muted, and the since column says when the
  // agent was last seen. A server whose agent is offline is shown as before.
  describe('client hosts', () => {
    const agent = (hostId: string, status: 'online' | 'offline', lastSeen: string) => ({
      host_id: hostId, host_name: hostId, agent_name: hostId, status, last_seen: lastSeen, created_at: '2026-01-01T00:00:00Z',
      secret_names: null, secrets_state: null, local_check_problems: null, version_status: 'current',
    });
    const threeHoursAgo = () => new Date(Date.now() - 3 * 3600_000).toISOString();
    function routes() {
      return {
        'GET /api/v1/orgs/acme': org('viewer'),
        'GET /api/v1/orgs/acme/zones': ZONES,
        'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
        'GET /api/v1/orgs/acme/fleet': {
          hosts: [
            host({ host_id: 'h-off', name: 'laptop-1', state: 'crit', crit: 2, kind: 'client' }),
            host({ host_id: 'h-srv', name: 'web-1', state: 'warn', warn: 1, kind: 'server' }),
            host({ host_id: 'h-on', name: 'laptop-2', state: 'ok', kind: 'client' }),
          ],
          totals: { crit: 1, warn: 1, unknown: 0, ok: 1, no_data: 0, all_ignored: 0 },
        },
        'GET /api/v1/orgs/acme/agents': {
          agents: [agent('h-off', 'offline', threeHoursAgo()), agent('h-srv', 'offline', threeHoursAgo()), agent('h-on', 'online', new Date().toISOString())],
        },
      };
    }
    const row = (name: string) => screen.getByRole('link', { name: new RegExp(`^${name}`) }).closest('[data-list-row]') as HTMLElement;

    it('shows an offline client as Off, its last state muted, and when it was last seen', async () => {
      mockApi(routes());
      renderPage();
      await screen.findByRole('link', { name: /^laptop-1/ });

      await waitFor(() => expect(within(row('laptop-1')).getByText('Off')).toBeInTheDocument());
      expect(within(row('laptop-1')).queryByText('crit')).not.toBeInTheDocument();
      // Visible, not only a tooltip: touch and screen-reader users read it too.
      expect(within(row('laptop-1')).getByText('last state: crit')).toBeInTheDocument();
      expect(within(row('laptop-1')).getByText('last seen 3 h ago')).toBeInTheDocument();
    });

    it('leaves a server with an offline agent alone and tags an online client', async () => {
      mockApi(routes());
      renderPage();
      await screen.findByRole('link', { name: /^laptop-1/ });
      await waitFor(() => expect(within(row('laptop-1')).getByText('Off')).toBeInTheDocument());

      expect(within(row('web-1')).getByText('warn')).toBeInTheDocument();
      expect(within(row('web-1')).queryByText('Off')).not.toBeInTheDocument();
      expect(within(row('web-1')).queryByText('Client')).not.toBeInTheDocument();
      expect(within(row('laptop-2')).getByText('ok')).toBeInTheDocument();
      expect(within(row('laptop-2')).getByText('Client')).toBeInTheDocument();
      expect(within(row('laptop-2')).queryByText('Off')).not.toBeInTheDocument();
    });
  });
});
