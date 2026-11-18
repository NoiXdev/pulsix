import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { HostChecksCard } from './HostChecksCard.tsx';

const NOW = '2026-09-27T12:00:00Z';

const value = (over: Record<string, unknown>) => ({ item: '', at: NOW, stale: false, state: 'ok', state_stale: false, flapping: false, ignored: false, ...over });
const check = (over: Record<string, unknown>) => ({
  definition_id: 'd1',
  params: {},
  interval_seconds: 60,
  enabled: true,
  origin: 'seed',
  source: 'org',
  values: [],
  ...over,
});

const CHECKS = {
  host_id: 'h1',
  monitored: true,
  checks: [
    check({ key: 'cpu', plugin: 'cpu', values: [value({ metric: 'cpu.used_pct', value: 12.5 })] }),
    check({
      key: 'disk',
      plugin: 'disk',
      interval_seconds: 300,
      source: 'group',
      source_name: 'Web tier',
      values: [
        value({ item: '/', metric: 'disk.used_pct', value: 40 }),
        value({ item: '/var', metric: 'disk.used_pct', value: 91, state: 'warn' }),
      ],
    }),
    check({
      key: 'site',
      plugin: 'http',
      source: 'host',
      source_name: 'web-1',
      values: [
        value({ metric: 'http.up', label: 'up' }),
        value({ metric: 'http.status_code', value: 503, state: 'crit' }),
        value({ metric: 'http.response_ms', value: 210 }),
      ],
    }),
    check({ key: 'cert', plugin: 'tls', values: [value({ metric: 'tls.cert_expiry_days', value: 41 })] }),
    check({ key: 'quiet', plugin: 'uptime', values: [] }),
  ],
};

function renderCard(opts: { canRead?: boolean; canWrite?: boolean; frozen?: boolean } = {}) {
  const { canRead = true, canWrite = true, frozen = false } = opts;
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const hostRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/hosts/$id',
    component: () => <HostChecksCard org="acme" hostId="h1" canRead={canRead} canWrite={canWrite} frozen={frozen} />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([hostRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/hosts/h1'] }),
  });
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <ConfirmProvider>
          <RouterProvider router={router} />
        </ConfirmProvider>
      </ToastProvider>
    </QueryClientProvider>,
  );
}

function mock(checks: unknown = CHECKS) {
  mockApi({
    'GET /api/v1/orgs/acme/hosts/h1/checks': checks,
    'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
    'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
    'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
  });
}

/** The table's body rows, one per check. */
const bodies = async () => (await screen.findAllByRole('heading', { level: 3 })).map((h) => h.closest('tbody') as HTMLElement);
const names = async () => (await screen.findAllByRole('heading', { level: 3 })).map((h) => h.textContent);
const toggle = (key: string) => screen.getByRole('button', { name: key });

describe('HostChecksCard — compact table', () => {
  it('lists failing checks first and the rest in the given order', async () => {
    mock();
    renderCard();
    expect(await names()).toEqual(['disk', 'site', 'cpu', 'cert', 'quiet']);
  });

  it('shows source, interval, worst status and a key value per row', async () => {
    mock();
    renderCard();
    const [disk, site, cpu, cert, quiet] = await bodies();
    const cells = (b: HTMLElement) => within(b).getAllByRole('cell').slice(0, 5).map((c) => c.textContent);
    expect(cells(disk)).toEqual(['diskdisk', 'Web tier', 'every 5 min', 'warn', '91 % on /var']);
    expect(cells(site)[3]).toBe('crit');
    expect(cells(site)[4]).toBe('503 · 210 ms');
    expect(cells(cpu)[1]).toBe('Organization');
    expect(cells(cpu)[3]).toBe('ok');
    expect(cells(cpu)[4]).toBe('12.5 %');
    expect(cells(cert)[4]).toBe('41 days left');
    expect(cells(quiet)[3]).toBe('no value');
    expect(cells(quiet)[4]).toBe('—');
  });

  it('opens only the first failing check, and the others by button', async () => {
    const user = userEvent.setup();
    mock();
    renderCard();
    await names();
    expect(toggle('disk')).toHaveAttribute('aria-expanded', 'true');
    expect(toggle('site')).toHaveAttribute('aria-expanded', 'false');
    expect(toggle('cpu')).toHaveAttribute('aria-expanded', 'false');
    expect(screen.getByText('/var disk.used_pct')).toBeInTheDocument();
    expect(screen.queryByText('cpu.used_pct')).not.toBeInTheDocument();

    await user.click(toggle('cpu'));
    expect(toggle('cpu')).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('cpu.used_pct')).toBeInTheDocument();

    await user.click(toggle('cpu'));
    expect(toggle('cpu')).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('cpu.used_pct')).not.toBeInTheDocument();
  });

  it('opens with the keyboard and keeps the item actions in the opened rows', async () => {
    const user = userEvent.setup();
    mock();
    renderCard();
    await names();
    toggle('cpu').focus();
    await user.keyboard('{Enter}');
    expect(toggle('cpu')).toHaveAttribute('aria-expanded', 'true');
    // Whole-host metrics have no item to ignore; the graph is there.
    expect(screen.getByRole('button', { name: 'Graph cpu cpu.used_pct' })).toBeInTheDocument();
    // The disk row that opened by itself carries its item decision.
    expect(screen.getByRole('button', { name: 'Ignore disk /var' })).toBeInTheDocument();
  });

  it('shows the http assertions inside the opened row', async () => {
    const user = userEvent.setup();
    mock({
      ...CHECKS,
      checks: [check({ key: 'site', plugin: 'http', params: { assertions: ['status=status == 200'] }, values: [value({ metric: 'http.status_code', value: 200 })] })],
    });
    renderCard();
    await names();
    await user.click(toggle('site'));
    expect(screen.getByRole('table', { name: 'Assertions of site' })).toBeInTheDocument();
  });

  it('is read-only without checks.write: no item decisions, readings still open', async () => {
    const user = userEvent.setup();
    mock();
    renderCard({ canWrite: false });
    await names();
    expect(screen.queryByRole('button', { name: /^Ignore / })).not.toBeInTheDocument();
    await user.click(toggle('cpu'));
    expect(screen.getByText('cpu.used_pct')).toBeInTheDocument();
  });

  it('says why nothing is judged for a frozen client, keeps the order and opens nothing', async () => {
    mock();
    renderCard({ frozen: true });
    expect(await names()).toEqual(['cpu', 'disk', 'site', 'cert', 'quiet']);
    expect(screen.getByText(/Not judged while the client is off/)).toBeInTheDocument();
    expect(screen.getAllByText('not judged')).toHaveLength(5);
    for (const key of ['cpu', 'disk', 'site']) expect(toggle(key)).toHaveAttribute('aria-expanded', 'false');
  });

  it('reads an empty set as empty, without a table', async () => {
    mock({ host_id: 'h1', monitored: true, checks: [] });
    renderCard();
    expect(await screen.findByText('No checks resolve for this host yet.')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('has no readings to open for an unmonitored host', async () => {
    mock({ ...CHECKS, monitored: false });
    renderCard();
    await names();
    expect(screen.queryByRole('button', { name: 'cpu' })).not.toBeInTheDocument();
    expect(screen.getByText(/not being monitored/)).toBeInTheDocument();
  });
});
