import type { ReactNode } from 'react';
import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router';
import { ToastProvider } from '../ui/toast.tsx';
import type { MockRoute } from './mockApi.ts';
import { SearchEcho } from './SearchEcho.tsx';

/**
 * Shared fixtures for the host page's tests (HostDetailPage and the
 * sidebar and section components it is built from): one host, the org it
 * belongs to, every GET the page makes with a quiet default answer, and a
 * router that has the routes the page links to.
 */

export const HOST = {
  id: 'h1',
  name: 'web-04.fra',
  zone_id: 'z1',
  address: '203.0.113.4',
  managed: false,
  monitored: false,
  revision: 3,
  created_at: '2026-01-01T00:00:00Z',
  last_seen_at: '2026-03-01T00:00:00Z',
  facts: { os: 'linux', os_version: 'debian 13' } as Record<string, unknown>,
};

export type HostFixture = typeof HOST;

export function orgBody(role = 'admin') {
  return { org: { id: 'o1', name: 'Acme', slug: 'acme', role, created_at: '2026-01-01T00:00:00Z' } };
}

export const NOW = '2026-11-03T12:00:00Z';

/** A check value as the host-checks route returns it. */
export function reading(metric: string, value: number | undefined, over: Record<string, unknown> = {}) {
  return { item: '', metric, value, at: NOW, stale: false, state: 'ok', state_stale: false, flapping: false, ignored: false, ...over };
}

/** A check as the host-checks route returns it. */
export function check(key: string, plugin: string, values: unknown[], over: Record<string, unknown> = {}) {
  return {
    definition_id: `d-${key}`,
    key,
    plugin,
    params: {},
    interval_seconds: 60,
    enabled: true,
    origin: 'seed',
    source: 'org',
    values,
    ...over,
  };
}

export function alertBody(id: string, severity: 'warn' | 'crit' | 'unknown') {
  return {
    id,
    host_id: 'h1',
    host_name: 'web-04.fra',
    scope: 'tuple',
    check_key: 'disk',
    item: '/var',
    metric: 'disk.used_pct',
    opened_at: NOW,
    severity,
    peak_severity: severity,
    snooze_until_change: false,
    was_flapping: false,
    muted: false,
  };
}

/** hostRoutes answers every read the host page makes. `host` patches the
 * host, `role` the caller's role; `extra` adds or replaces routes. */
export function hostRoutes(
  { host = {}, role = 'admin' }: { host?: Partial<HostFixture>; role?: string } = {},
  extra: Record<string, MockRoute> = {},
): Record<string, MockRoute> {
  const h = { ...HOST, ...host };
  return {
    'GET /api/v1/orgs/acme': orgBody(role),
    'GET /api/v1/orgs/acme/hosts/h1': { host: h },
    'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [] },
    'GET /api/v1/orgs/acme/hosts/h1/checks': { host_id: 'h1', monitored: h.monitored, checks: [] },
    'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
    'GET /api/v1/orgs/acme/hosts/h1/alert-routes': { routes: [] },
    'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    'GET /api/v1/orgs/acme/hosts/h1/inventory/groups': { groups: [] },
    'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
    'GET /api/v1/orgs/acme/bastions': { bastions: [] },
    'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': { vars: {}, connection: { host_id: 'h1', bastion_source: 'none' }, warnings: [] },
    'GET /api/v1/orgs/acme/hosts/h1/agent': () =>
      new Response(JSON.stringify({ detail: 'no agent' }), { status: 404, headers: { 'Content-Type': 'application/json' } }),
    'GET /api/v1/orgs/acme/alerts': { alerts: [], next_cursor: '' },
    'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '' },
    'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', name: 'Frankfurt', slug: 'fra-1' }] },
    ...extra,
  };
}

/** renderAt mounts `page` at /o/acme/hosts/h1 inside a router that also has
 * the host list, the alerts page and the runs page (each echoing its
 * `?host=`), so links and navigation off the page can be followed. */
export function renderAt(page: () => ReactNode, qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts', component: () => <div>HOSTS PAGE</div> });
  const detailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: page });
  const search = (s: Record<string, unknown>) => ({ host: typeof s.host === 'string' ? s.host : undefined });
  const alertsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/alerts',
    validateSearch: search,
    component: () => <SearchEcho name="ALERTS PAGE" />,
  });
  const runsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/runs',
    validateSearch: search,
    component: () => <SearchEcho name="RUNS PAGE" />,
  });
  const runRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN PAGE</div> });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks', component: () => <div>CHECKS PAGE</div> });
  const routeTree = rootRoute.addChildren([
    orgRoute.addChildren([hostsRoute, detailRoute, alertsRoute, runsRoute, runRoute, checksRoute]),
  ]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }) });

  const result = render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...result, router, qc };
}
