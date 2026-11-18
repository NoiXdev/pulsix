import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { expandChecks } from '../../test/expandChecks.ts';
import { mockApi } from '../../test/mockApi.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { HostChecksCard } from './HostChecksCard.tsx';

const AT = new Date(Date.now() - 60_000).toISOString();

const v = (item: string, metric: string, over: Record<string, unknown>) => ({
  item, metric, at: AT, stale: false, state: null, flapping: false, state_stale: false, ignored: false, ...over,
});

/** An http check that is down for an assertion's sake, and a tcp check
 * that is down. */
const CHECKS = {
  host_id: 'h1',
  monitored: true,
  checks: [
    {
      definition_id: 'd1',
      key: 'web',
      plugin: 'http',
      params: { url: 'https://example.com/', assertions: ['ok=status in 200..299', 'title=html("h1") contains "Shop" @warn'] },
      interval_seconds: 60,
      enabled: true,
      origin: 'custom',
      source: 'host',
      values: [
        v('', 'http.up', { value: 1 }),
        v('', 'http.failure', { label: 'none' }),
        v('', 'http.dns_ms', { value: 4 }),
        v('', 'http.connect_ms', { value: 11 }),
        v('', 'http.ttfb_ms', { value: 85 }),
        v('', 'http.transfer_ms', { value: 2 }),
        v('ok', 'http.assertion', { label: 'pass' }),
        v('ok', 'http.assertion_actual', { label: '200' }),
        v('title', 'http.assertion', { label: 'warn' }),
        v('title', 'http.assertion_actual', { label: '"Welcome"' }),
      ],
    },
    {
      definition_id: 'd2',
      key: 'port',
      plugin: 'tcp',
      params: { port: 443 },
      interval_seconds: 60,
      enabled: true,
      origin: 'custom',
      source: 'host',
      values: [v('', 'tcp.up', { value: 0 }), v('', 'tcp.failure', { label: 'refused' })],
    },
  ],
};

async function renderCard(locale: 'en' | 'de' = 'en') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    component: () => <HostChecksCard org="acme" hostId="h1" canRead canWrite={false} />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }),
  });
  const view = render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale={locale}>
        <ToastProvider>
          <ConfirmProvider>
            <RouterProvider router={router} />
          </ConfirmProvider>
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
  await expandChecks();
  return view;
}

const block = (key: string) => within(screen.getByRole('heading', { name: key }).closest('tbody') as HTMLElement);

const ROUTES = {
  'GET /api/v1/orgs/acme/hosts/h1/checks': CHECKS,
  'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
  'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
};

const cells = (table: HTMLElement) =>
  within(table).getAllByRole('row').slice(1).map((r) => within(r).getAllByRole('cell').map((c) => c.textContent));

describe('HostChecksCard — web checks', () => {
  it("shows an http check's timing breakdown and its assertions under it", async () => {
    mockApi(ROUTES);
    await renderCard();
    const bar = await screen.findByRole('figure', { name: 'Timing of web' });
    expect(within(bar).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['DNS4 ms', 'connect11 ms', 'first byte85 ms', 'transfer2 ms']);
    const table = screen.getByRole('table', { name: 'Assertions of web' });
    expect(cells(table)).toEqual([
      ['ok', 'status in 200..299', 'crit', 'pass', '200', '1 minute ago'],
      ['title', 'html("h1") contains "Shop"', 'warn', 'warn', '"Welcome"', '1 minute ago'],
    ]);
    expect(block('port').queryByRole('figure')).not.toBeInTheDocument();
  });

  it('words the failure reason next to <plugin>.up and the result labels in the values', async () => {
    mockApi(ROUTES);
    await renderCard();
    await screen.findByRole('figure', { name: 'Timing of web' });
    expect(block('port').getByTestId('failure-reason')).toHaveTextContent('connection refused');
    expect(block('port').getByTestId('failure-reason').closest('li')).toHaveTextContent(/^tcp\.up/);
    // The failure value's own row reads in words too.
    expect(block('port').getAllByText('connection refused')).toHaveLength(2);
    expect(block('web').queryByTestId('failure-reason')).not.toBeInTheDocument();
    expect(block('web').getByText('no failure')).toBeInTheDocument();
  });

  it('speaks German', async () => {
    mockApi(ROUTES);
    await renderCard('de');
    const table = await screen.findByRole('table', { name: 'Zusicherungen von web' });
    expect(cells(table)[0].slice(2, 4)).toEqual(['kritisch', 'bestanden']);
    expect(screen.getByRole('figure', { name: 'Zeitaufteilung von web' })).toHaveTextContent('Verbindung11 ms');
    expect(block('port').getByTestId('failure-reason')).toHaveTextContent('Verbindung abgelehnt');
  });
});
