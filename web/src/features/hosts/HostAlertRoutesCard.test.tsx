import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { mockApi } from '../../test/mockApi.ts';
import { HostAlertRoutesCard } from './HostAlertRoutesCard.tsx';

const route = (over: Record<string, unknown> = {}) => ({
  id: 'r1',
  name: 'all',
  enabled: true,
  scope: 'org',
  severities: ['warn', 'crit'],
  events: ['opened'],
  channels: ['c1'],
  revision: 1,
  created_at: '',
  updated_at: '',
  ...over,
});

/** Same minimal host-route-plus-destination-route tree HostChecksCard.test.tsx
 * and HostAlertsCard.test.tsx already build for their own "manage this
 * elsewhere" link — the card links to Settings → Alert routes
 * (`/o/$org/settings/alert-routes`), which needs a real router context to
 * resolve. */
function renderCard(canRead = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const settingsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings' });
  const alertRoutesRoute = createRoute({
    getParentRoute: () => settingsRoute,
    path: '/alert-routes',
    component: () => <div>ALERT ROUTES PAGE</div>,
  });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    component: () => <HostAlertRoutesCard org="acme" hostId="h1" canRead={canRead} />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([settingsRoute.addChildren([alertRoutesRoute]), hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('HostAlertRoutesCard', () => {
  it('names the routes that cover this host', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': { routes: [route({ id: 'r1', name: 'all', scope: 'org' })] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    expect(await screen.findByText('all')).toBeInTheDocument();
    expect(screen.getByText('Organization')).toBeInTheDocument();
  });

  it('says so when no route covers this host, rather than an empty box', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': { routes: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    // Decision 8: "nobody is told about this host" must be stated, not
    // left as an empty box a reader mistakes for "not loaded yet".
    expect(await screen.findByText(/no route covers this host/i)).toBeInTheDocument();
  });

  it('names every route covering this host, however many there are, by the monitoring group\'s own name', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': {
        routes: [route({ id: 'r1', name: 'org default', scope: 'org' }), route({ id: 'r2', name: 'web tier pages', scope: 'monitoring_group', scope_id: 'g1' })],
      },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 10, created_at: '', updated_at: '' }] },
    });
    renderCard();

    expect(await screen.findByText('org default')).toBeInTheDocument();
    expect(screen.getByText('web tier pages')).toBeInTheDocument();
    expect(screen.getByText('Web tier')).toBeInTheDocument();
  });

  it('shows what each covering route fires on, so a silent route is explicable', async () => {
    // The union view's whole reason to exist is "why did no mail
    // arrive?" (Service.ForHost's own doc comment, backlog 4.1b). ForHost
    // deliberately returns a route that covers this host whatever its
    // severity and event selections are, so the answer is usually "it
    // only fires on crit" — which the card has to show for the question
    // to be answerable here at all.
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': {
        routes: [route({ name: 'crit only', severities: ['crit'], events: ['opened', 'closed'] })],
      },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    expect(await screen.findByText('crit only')).toBeInTheDocument();
    expect(screen.getByText('Critical')).toBeInTheDocument();
    expect(screen.getByText('opened, closed')).toBeInTheDocument();
  });

  it('says a covering route whose last channel was deleted reaches nobody', async () => {
    // Decision 8, in the union view: a route that covers this host and
    // has no channel left is the worst case the card exists to catch —
    // it looks like coverage and delivers nothing.
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': { routes: [route({ name: 'orphaned', channels: [] })] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    expect(await screen.findByText('orphaned')).toBeInTheDocument();
    expect(screen.getByText(/reaches nobody/i)).toBeInTheDocument();
  });

  it('links to Settings → Alert routes', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': { routes: [route()] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    await screen.findByText('all');
    expect(screen.getByRole('link', { name: /alert routes/i })).toHaveAttribute('href', '/o/acme/settings/alert-routes');
  });

  it('asks for nothing without channels.read', async () => {
    const fetch = mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': { routes: [route()] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard(false);

    expect(await screen.findByText(/visible to members/i)).toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('shows a read error rather than hanging on "Loading…" forever', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': () => new Response('{"detail":"boom"}', { status: 500 }),
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    expect(await screen.findByRole('alert')).toHaveTextContent(/boom/i);
  });

  it('names a covering route\'s own ladder by rung count and the time to its last rung, without opening the editor', async () => {
    // v0.4d Task 8: "who gets woken, and how late" has to be readable right
    // here — delay_seconds is relative to the rung before it, so the total
    // to the last rung (15 min: 5 then 10) is not the same number as
    // either rung's own delay, and a reader should not have to add them up.
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': {
        routes: [
          route({
            name: 'pages on-call',
            steps: [
              { position: 1, delay_seconds: 300, channel_ids: ['c1'] },
              { position: 2, delay_seconds: 600, channel_ids: ['c1'] },
            ],
          }),
        ],
      },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    expect(await screen.findByText('pages on-call')).toBeInTheDocument();
    expect(screen.getByText(/2 rungs/i)).toBeInTheDocument();
    expect(screen.getByText(/after 15 min/i)).toBeInTheDocument();
  });

  it('calls out two covering routes that each escalate on their own — the overlap the spec names as a risk', async () => {
    // A route's ladder answers "when does THIS route escalate" — nothing
    // merges the two, so an operator who only read one ladder would be
    // surprised to be paged again by the other. This has to say so
    // explicitly rather than leaving two "Escalates over N rungs" lines to
    // be noticed and cross-referenced by eye.
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': {
        routes: [
          route({ id: 'r1', name: 'pages on-call', steps: [{ position: 1, delay_seconds: 300, channel_ids: ['c1'] }] }),
          route({ id: 'r2', name: 'pages manager', steps: [{ position: 1, delay_seconds: 900, channel_ids: ['c1'] }] }),
        ],
      },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    await screen.findByText('pages on-call');
    expect(screen.getByText(/2 routes below escalate independently/i)).toBeInTheDocument();
  });

  it('does not warn about overlapping escalation when only one covering route has a ladder', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/alert-routes': {
        routes: [
          route({ id: 'r1', name: 'pages on-call', steps: [{ position: 1, delay_seconds: 300, channel_ids: ['c1'] }] }),
          route({ id: 'r2', name: 'plain route' }),
        ],
      },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    });
    renderCard();

    await screen.findByText('pages on-call');
    expect(screen.queryByText(/escalate independently/i)).not.toBeInTheDocument();
  });
});
