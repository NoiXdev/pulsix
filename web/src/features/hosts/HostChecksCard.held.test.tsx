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

/**
 * Backlog 3.9b ruling: a value held on its item's state (a sleeping disk's
 * sector count) keeps its last reading and verdict; the row says what it
 * waits on and offers nothing a stale row would.
 */
const AT = new Date(Date.now() - 60_000).toISOString();
const OLD = new Date(Date.now() - 8 * 24 * 3600_000).toISOString();

const v = (item: string, metric: string, over: Record<string, unknown> = {}) => ({
  item, metric, at: AT, stale: false, state: null, flapping: false, state_stale: false, ignored: false, vanished: false, ...over,
});

const smart = (values: unknown[]) => ({
  host_id: 'h1',
  monitored: true,
  checks: [
    {
      definition_id: 'd1', key: 'smart', plugin: 'smart', params: {},
      interval_seconds: 300, enabled: true, origin: 'custom', source: 'host', values,
    },
  ],
});

const HELD = [
  v('/dev/sdd', 'smart.power_mode', { label: 'standby', state: 'ok' }),
  v('/dev/sdd', 'smart.reallocated_sectors', { value: 3, state: 'crit', at: OLD, evaluated_at: OLD, held_while: 'standby' }),
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

describe('held rows (3.9b)', () => {
  it('keeps the verdict, says what the value waits on and offers no Forget', async () => {
    mockApi(routes(smart(HELD)));
    await renderCard(true);
    const row = (await screen.findByText('/dev/sdd smart.reallocated_sectors')).closest('li') as HTMLElement;
    expect(within(row).getByText('waiting while standby')).toBeInTheDocument();
    expect(within(row).getByText('3')).toBeInTheDocument();
    expect(within(row).queryByText('stale')).not.toBeInTheDocument();
    expect(within(row).queryByText('stale verdict')).not.toBeInTheDocument();
    expect(within(row).queryByRole('button', { name: /Forget/ })).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    mockApi(routes(smart(HELD)));
    await renderCard(false, 'de');
    const row = (await screen.findByText('/dev/sdd smart.reallocated_sectors')).closest('li') as HTMLElement;
    expect(within(row).getByText('wartet, solange standby')).toBeInTheDocument();
  });
});
