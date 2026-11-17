import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { HostsListPage } from './HostsListPage.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { validateListSearch } from '../lists/listSearch.ts';

const ZONES = { zones: [{ id: 'z1', slug: 'fra', name: 'Frankfurt' }] };
const PREFS = { density: 'standard', page_size: 25, lists: {} };
const host = (id: string, name: string) => ({
  id, name, zone_id: 'z1', address: '203.0.113.4', managed: true, monitored: false, last_seen_at: '2026-03-01T00:00:00Z', facts: {}, active_runs: 0,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts',
    validateSearch: (s: Record<string, unknown>) => ({ ...validateListSearch(s), q: typeof s.q === 'string' ? s.q : undefined }),
    component: HostsListPage,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([hostsRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/hosts'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="de">
        <ConfirmProvider>
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </ConfirmProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('HostsListPage in German', () => {
  it('writes the page and a plural count in German', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-04.fra'), { ...host('h2', 'db-01.fra'), active_runs: 2 }], next_cursor: '', total: 2 },
      'GET /api/v1/orgs/acme/zones': ZONES,
    });
    renderPage();

    expect(await screen.findByText('1–2 von 2 Hosts')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Host hinzufügen' })).toBeInTheDocument();
    expect(screen.getByText('Verwaltet · Ansible')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Mit aktiven Läufen' })).toBeInTheDocument();
    expect(screen.getByText('Aktive Läufe')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '2 laufen' })).toBeInTheDocument();
  });

  it('uses the German singular for one host', async () => {
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ ...host('h1', 'web-04.fra'), active_runs: 1 }], next_cursor: '', total: 1 },
      'GET /api/v1/orgs/acme/zones': ZONES,
    });
    renderPage();

    expect(await screen.findByText('1–1 von 1 Host')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '1 läuft' })).toBeInTheDocument();
  });
  it('says Aus and when an offline client was last seen, and tags an online one', async () => {
    const agent = (hostId: string, status: 'online' | 'offline', lastSeen: string) => ({
      host_id: hostId, host_name: hostId, agent_name: hostId, status, last_seen: lastSeen, created_at: '2026-01-01T00:00:00Z',
      secret_names: null, secrets_state: null, local_check_problems: null, version_status: 'current',
    });
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ ...host('h1', 'laptop-1'), kind: 'client' }, { ...host('h2', 'laptop-2'), kind: 'client' }], next_cursor: '', total: 2 },
      'GET /api/v1/orgs/acme/zones': ZONES,
      'GET /api/v1/orgs/acme/agents': {
        agents: [agent('h1', 'offline', new Date(Date.now() - 3 * 3600_000).toISOString()), agent('h2', 'online', new Date().toISOString())],
      },
    });
    renderPage();

    expect(await screen.findByText('Aus')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'laptop-1, Aus' })).toBeInTheDocument();
    expect(screen.getByText('zuletzt gesehen vor 3 Std.')).toBeInTheDocument();
    expect(screen.getByText('Client')).toBeInTheDocument();
  });
});
