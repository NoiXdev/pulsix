import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { ChannelsPage } from './ChannelsPage.tsx';
import { formatElapsed } from './steps.ts';
import { de } from '../../i18n/de/index.ts';

const org = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'admin', created_at: '' } };
const hook = {
  id: 'c1', kind: 'webhook', name: 'ops hook', enabled: true, revision: 2, created_at: '', updated_at: '',
  webhook: { method: 'POST', url: 'https://hooks.example.test/…', headers: [], signed: false },
};
const digest = {
  digest: { enabled: true, weekday: 'mon', time: '08:00', timezone: 'Pacific/Auckland', channels: [], next_run_at: '2026-07-06T20:00:00Z' },
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/settings/notifications', component: ChannelsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/notifications'] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="de">
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('ChannelsPage in German', () => {
  it('writes the page, the delivery count and the digest date in German, and keeps the digest zone', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org,
      'GET /api/v1/orgs/acme/notification-channels': { channels: [hook], mail_available: false },
      'GET /api/v1/orgs/acme/notification-digest': digest,
      'GET /api/v1/orgs/acme/notification-channels/c1/deliveries': {
        deliveries: [
          { id: 'd1', event_kind: 'run.finished', status: 'pending', attempts: 3, last_error: 'the endpoint answered 503', created_at: '2026-09-22T08:00:00Z' },
        ],
      },
    });
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Benachrichtigungen' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Kanal hinzufügen' })).toBeInTheDocument();
    expect(screen.getByText('aktiviert')).toBeInTheDocument();
    // mail_available is false: the hint is written in German too.
    expect(screen.getByText('E-Mail ist auf dieser Installation nicht eingerichtet')).toBeInTheDocument();

    // The digest's next run is written in the digest's own zone — 08:00 on
    // 7 July in Auckland — in the German format, and names that zone.
    expect(await screen.findByText('nächster Versand: 07.07.2026, 08:00 (Pacific/Auckland)')).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Wochenzusammenfassung' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Aktionen für ops hook' }));
    await user.click(screen.getByRole('menuitem', { name: 'Zustellungen' }));
    const dialog = await screen.findByRole('dialog', { name: 'Zustellungen · ops hook' });
    expect(await within(dialog).findByText(/3 Versuche · the endpoint answered 503/)).toBeInTheDocument();
    expect(within(dialog).getByText('ausstehend')).toBeInTheDocument();
  });

  it('names an escalation step after the previous one in German', () => {
    expect(formatElapsed(0, de)).toBe('sofort');
    expect(formatElapsed(15 * 60, de)).toBe('nach 15 min');
    expect(formatElapsed(90 * 60, de)).toBe('nach 1 h 30 min');
    expect(de.notifications.escalatesOver(1, 'nach 5 min')).toBe('Eskaliert über 1 Stufe, die letzte nach 5 min');
    expect(de.notifications.escalatesOver(3, 'nach 20 min')).toBe('Eskaliert über 3 Stufen, die letzte nach 20 min');
  });
});
