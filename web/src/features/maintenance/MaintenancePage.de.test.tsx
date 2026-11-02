import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { formatter } from '../../i18n/format.ts';
import { mockApi } from '../../test/mockApi.ts';
import { MaintenancePage } from './MaintenancePage.tsx';
import type { MaintenanceWindow } from '../../api/types.ts';

const org = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } };
const nightly: MaintenanceWindow = {
  id: 'w1', name: 'nightly', target_kind: 'org', reason: 'maintenance', cron: '0 2 * * 6', description: 'Sa um 02:00', duration: '2h',
  timezone: 'Europe/Berlin', active_now: true, revision: 1, created_at: '', updated_at: '',
};
const dbUpgrade: MaintenanceWindow = {
  id: 'w2', name: 'db upgrade', target_kind: 'host', reason: 'silence', target_id: 'h1', target_name: 'db-1', starts_at: '2026-09-22T10:00:00Z',
  ends_at: '2026-09-22T12:00:00Z', active_now: false, revision: 3, created_at: '', updated_at: '',
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/maintenance', component: MaintenancePage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/maintenance'] }),
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

describe('MaintenancePage in German', () => {
  it('writes the active count label first, and keeps a window\'s explicit time zone and the German date format', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org, 'GET /api/v1/orgs/acme/maintenance-windows': { windows: [nightly, dbUpgrade] } });
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Wartung' })).toBeInTheDocument();
    expect(screen.getByText(/^Jetzt aktiv: 1 · Wartung lässt Zeitpläne Hosts überspringen/)).toBeInTheDocument();

    // A recurring window names its own zone, which no locale may drop.
    const row = screen.getByText('nightly').closest('[data-row]')!.parentElement as HTMLElement;
    expect(within(row).getByText('Sa um 02:00 · für 2h · Europe/Berlin')).toBeInTheDocument();
    expect(within(row).getByText('Gesamte Organisation')).toBeInTheDocument();

    // A one-off window is written in the browser's zone in the German date format.
    const de = formatter('de');
    const opts = { dateStyle: 'medium', timeStyle: 'short' } as const;
    expect(screen.getByText(`${de.dateTime('2026-09-22T10:00:00Z', opts)} – ${de.dateTime('2026-09-22T12:00:00Z', opts)}`)).toBeInTheDocument();
    expect(screen.getByText('Host db-1')).toBeInTheDocument();
    expect(screen.getByText('Stummschaltung')).toBeInTheDocument();
  });
});
