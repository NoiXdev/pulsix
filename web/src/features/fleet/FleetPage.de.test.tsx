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
});
