import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { HostsListPage } from './HostsListPage.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';

const ZONES = { zones: [{ id: 'z1', slug: 'fra', name: 'Frankfurt' }] };
const host = (id: string, name: string) => ({
  id, name, zone_id: 'z1', address: '203.0.113.4', managed: true, monitored: false, last_seen_at: '2026-03-01T00:00:00Z', facts: {},
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts',
    validateSearch: (s: Record<string, unknown>) => ({ q: typeof s.q === 'string' ? s.q : undefined }),
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
      'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-04.fra'), host('h2', 'db-01.fra')] },
      'GET /api/v1/orgs/acme/zones': ZONES,
    });
    renderPage();

    expect(await screen.findByText('2 Hosts')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Host hinzufügen' })).toBeInTheDocument();
    expect(screen.getByText('Verwaltet · Ansible')).toBeInTheDocument();
  });

  it('uses the German singular for one host', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/hosts': { hosts: [host('h1', 'web-04.fra')] }, 'GET /api/v1/orgs/acme/zones': ZONES });
    renderPage();

    expect(await screen.findByText('1 Host')).toBeInTheDocument();
  });
});
