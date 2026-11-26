import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { expandChecks } from '../../test/expandChecks.ts';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { HostChecksCard } from './HostChecksCard.tsx';

/**
 * Backlog 3.89 (assertion items get their actions in the assertion table)
 * and 3.88 (a vanished metric reads "No longer reported" and is forgotten
 * on its own).
 */
const AT = new Date(Date.now() - 60_000).toISOString();
const OLD = new Date(Date.now() - 3 * 3600_000).toISOString();

const v = (item: string, metric: string, over: Record<string, unknown> = {}) => ({
  item, metric, at: AT, stale: false, state: null, flapping: false, state_stale: false, ignored: false, vanished: false, ...over,
});

const rule = {
  id: 'r1', name: 'Assertion rule', enabled: true, origin: 'seed', scope: 'org', check_key: 'web', metric: 'http.assertion',
  severity: 'crit', mode: 'form', form: { metric: 'http.assertion', op: '>=', value: 1 }, source: 'http.assertion >= 1',
  for_seconds: 0, created_at: '', updated_at: '',
};

const web = (values: unknown[]) => ({
  host_id: 'h1',
  monitored: true,
  checks: [
    {
      definition_id: 'd1', key: 'web', plugin: 'http',
      params: { url: 'https://example.com/', assertions: ['ok=status in 200..299'] },
      interval_seconds: 60, enabled: true, origin: 'custom', source: 'host', values,
    },
  ],
});

const ASSERTION_VALUES = [
  v('', 'http.up', { value: 1, state: 'ok' }),
  v('ok', 'http.assertion', { label: 'pass', state: 'ok' }),
  v('ok', 'http.assertion_actual', { label: '200' }),
];

async function renderCard(canWrite: boolean, locale: 'en' | 'de' = 'en') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    component: () => <HostChecksCard org="acme" hostId="h1" canRead canWrite={canWrite} />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }),
  });
  render(
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
}

const routes = (checks: unknown, effective: unknown[] = []) => ({
  'GET /api/v1/orgs/acme/hosts/h1/checks': checks,
  'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective },
  'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
  'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
});

describe('assertion items on the host page (3.89)', () => {
  it('lists no generic rows for assertion items and offers a writer Ignore, Rules and no Forget on fresh ones', async () => {
    const user = userEvent.setup();
    mockApi(routes(web(ASSERTION_VALUES), [
      { check_key: 'web', item: 'ok', metric: 'http.assertion', severity: 'crit', observed: true, winner: rule, shadowed: [] },
    ]));
    await renderCard(true);
    const table = await screen.findByRole('table', { name: 'Assertions of web' });
    expect(screen.queryByText('ok http.assertion')).not.toBeInTheDocument();
    expect(screen.queryByText('ok http.assertion_actual')).not.toBeInTheDocument();
    expect(screen.getByText('http.up')).toBeInTheDocument();
    expect(within(table).getByRole('button', { name: 'Ignore web ok' })).toBeInTheDocument();
    expect(within(table).queryByRole('button', { name: 'Forget web ok' })).not.toBeInTheDocument();
    await user.click(within(table).getByRole('button', { name: 'Rules for web ok http.assertion' }));
    expect(await within(table).findByText('Assertion rule')).toBeInTheDocument();
  });

  it('offers Forget (item-wide) once every reading of the assertion item is stale', async () => {
    const user = userEvent.setup();
    let seen: MockRequest | undefined;
    mockApi({
      ...routes(web([
        v('', 'http.up', { value: 1 }),
        v('ok', 'http.assertion', { label: 'pass', stale: true, at: OLD }),
        v('ok', 'http.assertion_actual', { label: '200', stale: true, at: OLD }),
      ])),
      'DELETE /api/v1/orgs/acme/hosts/h1/items': (req: MockRequest) => {
        seen = req;
        return new Response(null, { status: 204 });
      },
    });
    await renderCard(true);
    const table = await screen.findByRole('table', { name: 'Assertions of web' });
    await user.click(within(table).getByRole('button', { name: 'Forget web ok' }));
    const dialog = await screen.findByRole('dialog', { name: 'Forget ok?' });
    await user.click(within(dialog).getByRole('button', { name: 'Forget' }));
    await waitFor(() => expect(seen).toBeDefined());
    expect(seen!.search.get('item')).toBe('ok');
    expect(seen!.search.has('metric')).toBe(false);
  });

  it('shows the ignored marker in the table', async () => {
    mockApi(routes(web([
      v('', 'http.up', { value: 1 }),
      v('ok', 'http.assertion', { label: 'pass', ignored: true, decided_by: { decision: 'ignore', scope: 'host' } }),
      v('ok', 'http.assertion_actual', { label: '200', ignored: true }),
    ])));
    await renderCard(true);
    const table = await screen.findByRole('table', { name: 'Assertions of web' });
    expect(within(table).getByText('Why ignored')).toBeInTheDocument();
    expect(within(table).getByRole('button', { name: 'Watch web ok' })).toBeInTheDocument();
  });

  it('gives a viewer no write actions', async () => {
    mockApi(routes(web([
      v('', 'http.up', { value: 1 }),
      v('ok', 'http.assertion', { label: 'pass', stale: true, at: OLD }),
      v('ok', 'http.assertion_actual', { label: '200', stale: true, at: OLD }),
    ])));
    await renderCard(false);
    const table = await screen.findByRole('table', { name: 'Assertions of web' });
    expect(within(table).queryByRole('button')).not.toBeInTheDocument();
    expect(within(table).queryByRole('columnheader', { name: 'Actions' })).not.toBeInTheDocument();
  });
});

describe('vanished rows (3.88)', () => {
  const VANISHED = [
    v('', 'http.up', { value: 1, state: 'ok' }),
    v('', 'http.body_match', { value: 1, stale: true, at: OLD, vanished: true, flapping: true, state: null }),
  ];

  it('mutes a vanished row, replaces the verdict with a marker and forgets only that metric', async () => {
    const user = userEvent.setup();
    let seen: MockRequest | undefined;
    mockApi({
      ...routes(web(VANISHED)),
      'DELETE /api/v1/orgs/acme/hosts/h1/items': (req: MockRequest) => {
        seen = req;
        return new Response(null, { status: 204 });
      },
    });
    await renderCard(true);
    const row = (await screen.findByText('http.body_match')).closest('li') as HTMLElement;
    expect(within(row).getByText('No longer reported')).toBeInTheDocument();
    expect(within(row).queryByText('stale')).not.toBeInTheDocument();
    expect(within(row).queryByText('flapping')).not.toBeInTheDocument();
    await user.click(within(row).getByRole('button', { name: 'Forget web http.body_match' }));
    const dialog = await screen.findByRole('dialog', { name: 'Forget http.body_match?' });
    expect(within(dialog).getByText(/Only this metric/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Forget' }));
    await waitFor(() => expect(seen).toBeDefined());
    expect(seen!.search.get('check_key')).toBe('web');
    expect(seen!.search.get('item')).toBe('');
    expect(seen!.search.get('metric')).toBe('http.body_match');
  });

  it('offers a viewer no Forget on it, and speaks German', async () => {
    mockApi(routes(web(VANISHED)));
    await renderCard(false, 'de');
    const row = (await screen.findByText('http.body_match')).closest('li') as HTMLElement;
    expect(within(row).getByText('Wird nicht mehr gemeldet')).toBeInTheDocument();
    expect(within(row).queryByRole('button', { name: /Vergessen/ })).not.toBeInTheDocument();
  });
});

describe('assertion items of a tcp check (3.87)', () => {
  it('lists them in the assertion table with the rules of tcp.assertion', async () => {
    const user = userEvent.setup();
    const tcpRule = { ...rule, metric: 'tcp.assertion', form: { metric: 'tcp.assertion', op: '>=', value: 1 }, source: 'tcp.assertion >= 1' };
    mockApi(routes({
      host_id: 'h1',
      monitored: true,
      checks: [{
        definition_id: 'd2', key: 'ssh', plugin: 'tcp',
        params: { port: 22, assertions: ['banner=response matches "^SSH-2" @warn'] },
        interval_seconds: 60, enabled: true, origin: 'custom', source: 'host',
        values: [
          v('', 'tcp.up', { value: 1, state: 'ok' }),
          v('banner', 'tcp.assertion', { label: 'pass', state: 'ok' }),
          v('banner', 'tcp.assertion_actual', { label: '"SSH-2.0-OpenSSH_9.6"' }),
        ],
      }],
    }, [{ check_key: 'ssh', item: 'banner', metric: 'tcp.assertion', severity: 'warn', observed: true, winner: tcpRule, shadowed: [] }]));
    await renderCard(true);
    const table = await screen.findByRole('table', { name: 'Assertions of ssh' });
    expect(within(table).getByText('response matches "^SSH-2"')).toBeInTheDocument();
    expect(within(table).getByText('"SSH-2.0-OpenSSH_9.6"')).toBeInTheDocument();
    expect(screen.queryByText('banner tcp.assertion')).not.toBeInTheDocument();
    await user.click(within(table).getByRole('button', { name: 'Rules for ssh banner tcp.assertion' }));
    expect(await within(table).findByText('Assertion rule')).toBeInTheDocument();
  });
});
