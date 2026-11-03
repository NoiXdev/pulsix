import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { BackupsPage } from './BackupsPage.tsx';

const ITEM = {
  name: 'pulsix-20261102T030000Z-manual.pxbak',
  created_at: '2026-11-02T12:00:00Z',
  kind: 'manual',
  actor_id: 'u1',
  actor_name: 'Ada',
  pulsix_version: '0.5.2',
  size_bytes: 1500000,
  state: 'succeeded',
  error: '',
  verified: true,
};

function renderGerman(item: Record<string, unknown>) {
  mockApi({
    'GET /api/v1/auth/me': {
      user: { id: 'u1', email: 'a@acme.io', display_name: 'A' },
      mfa_complete: true,
      mfa_enrolment_required: false,
      totp_confirmed: true,
      instance_admin: true,
    },
    'GET /api/v1/orgs/acme': { org: { id: 'o1', slug: 'acme', name: 'Acme', role: 'owner' } },
    'GET /api/v1/admin/backups/settings': {
      destination: 'folder',
      s3: { endpoint: '', region: '', bucket: '', prefix: '', access_key: '', secret_key_set: false, path_style: false },
      enabled: true,
      time_of_day: '03:00',
      time_zone: 'Europe/Berlin',
      retention: { daily: 7, weekly: 4, monthly: 6 },
      include_metrics: true,
    },
    'GET /api/v1/admin/backups': {
      items: [item],
      next_run_at: null,
      destination_label: 'Ordner /var/lib/pulsix/backups',
      storage_error: null,
    },
  });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const route = createRoute({ getParentRoute: () => orgRoute, path: '/settings/backups', component: BackupsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([route])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/backups'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <I18nProvider locale="de">
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('BackupsPage in German', () => {
  it('renders the page, its list and the restore dialog in German', async () => {
    const user = userEvent.setup();
    renderGerman(ITEM);

    expect(await screen.findByRole('heading', { name: 'Backups', level: 1 })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Jetzt sichern' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Verbindung testen' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Zeitplan und Aufbewahrung' })).toBeInTheDocument();
    expect(screen.getByText('manuell (Ada)')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Herunterladen' })).toBeInTheDocument();
    expect(screen.queryByText('Nicht bestätigt')).not.toBeInTheDocument();
    // Once in the status strip, once in the list.
    expect(screen.getAllByText(/2\.11\.2026/)).toHaveLength(2);
    expect(screen.getAllByText(/1,4 MB/)).toHaveLength(2);

    await user.click(screen.getByRole('button', { name: 'Wiederherstellen…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Backup wiederherstellen' });
    expect(within(dialog).getByText(/PULSIX_MASTER_KEY/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Master-Key/)).toBeInTheDocument();
  });

  it('marks a backup whose header cannot be authenticated as not confirmed', async () => {
    renderGerman({ ...ITEM, verified: false });

    const label = await screen.findByText('Nicht bestätigt');
    expect(label.closest('[title]')).toHaveAttribute('title', expect.stringMatching(/lassen sich nicht bestätigen/));
  });
});
