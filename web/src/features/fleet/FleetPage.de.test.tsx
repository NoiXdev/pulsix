import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { FleetPage } from './FleetPage.tsx';

const org = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } };

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const fleetRoute = createRoute({ getParentRoute: () => orgRoute, path: '/fleet', component: FleetPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([fleetRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/fleet'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="de">
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('FleetPage in German', () => {
  it('writes the totals as label: count, which agrees for any count', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org,
      'GET /api/v1/orgs/acme/zones': { zones: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/fleet': {
        hosts: [],
        totals: { crit: 3, warn: 1, unknown: 5, ok: 7, no_data: 2, all_ignored: 1 },
      },
    });
    renderPage();

    const totals = await screen.findByText(/nicht für die ganze Organisation/);
    expect(totals).toHaveTextContent(
      'kritisch: 3 · Warnung: 1 · unbekannt: 5 · ok: 7 · keine Daten: 2 · alle ignoriert: 1',
    );
    expect(screen.getByText('Keine Hosts in dieser Ansicht.')).toBeInTheDocument();
  });
  it('says Aus for an offline client and when it was last seen', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org,
      'GET /api/v1/orgs/acme/zones': { zones: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/fleet': {
        hosts: [{ host_id: 'h1', name: 'laptop-1', zone_id: 'z1', state: 'crit', crit: 1, warn: 0, unknown: 0, all_ignored: false, kind: 'client' }],
        totals: { crit: 1, warn: 0, unknown: 0, ok: 0, no_data: 0, all_ignored: 0 },
      },
      'GET /api/v1/orgs/acme/agents': {
        agents: [{
          host_id: 'h1', host_name: 'laptop-1', agent_name: 'laptop-1', status: 'offline', last_seen: new Date(Date.now() - 3 * 3600_000).toISOString(),
          created_at: '2026-01-01T00:00:00Z', secret_names: null, secrets_state: null, local_check_problems: null, version_status: 'current',
        }],
      },
    });
    renderPage();

    expect(await screen.findByText('Aus')).toBeInTheDocument();
    expect(screen.getByText('zuletzt gesehen vor 3 Std.')).toBeInTheDocument();
    expect(screen.getByText('letzter Zustand: kritisch')).toBeInTheDocument();
  });
});
