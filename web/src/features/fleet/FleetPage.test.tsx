import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
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

    await screen.findByText('db-1');
    // Rendered in the exact server order — this page must not silently
    // re-sort behind the API's own worst-first contract.
    const names = screen.getAllByRole('link', { name: /db-1|web-1|cache-1|lb-1|new-1|ignored-1/ }).map((el) => el.textContent);
    expect(names).toEqual(['db-1', 'web-1', 'cache-1', 'lb-1', 'new-1', 'ignored-1']);

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

    // Scoped by DOM structure, not by a CSS class name: FleetRow renders
    // the Link as GridRow's direct child, so its own parent element is the
    // row itself — a class-substring selector here would break silently
    // on an unrelated styling rename.
    const critRow = within(screen.getByRole('link', { name: 'db-1' }).parentElement as HTMLElement);
    expect(critRow.getByText('2')).toBeInTheDocument();
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
    const user = userEvent.setup();
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

    await screen.findByText('web-1');
    await user.selectOptions(screen.getByLabelText('Zone'), 'z2');

    await waitFor(() => expect(asked.at(-1)?.zone).toBe('z2'));
  });

  it('applies the monitoring group filter to the fleet request', async () => {
    const user = userEvent.setup();
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

    await screen.findByText('web-1');
    await user.selectOptions(screen.getByLabelText('Monitoring group'), 'g1');

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

    await screen.findByText('web-1');
    const input = screen.getByLabelText('Tag');
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
});
