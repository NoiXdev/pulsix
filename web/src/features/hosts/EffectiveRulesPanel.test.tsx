import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { EffectiveRulesPanel } from './EffectiveRulesPanel.tsx';

const rule = (over: Record<string, unknown> = {}) => ({
  id: 'r1',
  name: 'CPU usage warning',
  enabled: true,
  origin: 'seed',
  scope: 'org',
  check_key: 'cpu',
  metric: 'cpu.used_pct',
  severity: 'warn',
  mode: 'form',
  form: { metric: 'cpu.used_pct', op: '>=', value: 90 },
  source: 'cpu.used_pct >= 90',
  for_seconds: 0,
  created_at: '',
  updated_at: '',
  ...over,
});

const OBSERVED_ENTRY = {
  check_key: 'cpu',
  item: '',
  metric: 'cpu.used_pct',
  severity: 'warn',
  observed: true,
  winner: rule({ id: 'r1', name: 'Host override', scope: 'host', host_id: 'h1' }),
  shadowed: [rule({ id: 'r2', name: 'Org default warning', scope: 'org' })],
};

function renderPanel(canRead = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <EffectiveRulesPanel org="acme" hostId="h1" canRead={canRead} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

/**
 * EffectiveRulesPanel's scope narrowed after review: an observed tuple —
 * one HostChecksCard still lists a value for — is explained by a per-row
 * disclosure on that card (HostChecksCard.test.tsx covers it), not
 * repeated here. This panel is now the small remainder no `ValueRow` can
 * ever reach: an item-scoped rule whose item the host stopped reporting
 * (internal/rules.Resolve's own `Observed: false`).
 */
describe('EffectiveRulesPanel', () => {
  it('labels an item-scoped rule whose item the host no longer reports', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': {
        effective: [
          {
            check_key: 'disk',
            item: '/old-mount',
            metric: 'disk.used_pct',
            severity: 'crit',
            observed: false,
            winner: rule({
              id: 'r4', name: 'Old mount critical', scope: 'item', host_id: 'h1', item: '/old-mount',
              check_key: 'disk', metric: 'disk.used_pct', severity: 'crit',
            }),
            shadowed: [],
          },
        ],
      },
    });
    renderPanel();

    expect(await screen.findByText('no longer observed')).toBeInTheDocument();
    expect(screen.getByText('Old mount critical')).toBeInTheDocument();
    expect(screen.getByText('Item')).toBeInTheDocument();
  });

  // v0.3b-3: first_seen and last_seen now exist (GET /hosts/{id}/items), so
  // "no longer observed" can finally say since when — "not reported since
  // 14 February" is actionable where the bare label was not.
  it('names the date an item was last reported, once GET /hosts/{id}/items knows it', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': {
        items: [
          { check_key: 'disk', item: '/old-mount', first_seen: '2026-01-01T00:00:00Z', last_seen: '2026-02-14T08:00:00Z', reported: true, decision: 'watch' },
        ],
      },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': {
        effective: [
          {
            check_key: 'disk',
            item: '/old-mount',
            metric: 'disk.used_pct',
            severity: 'crit',
            observed: false,
            winner: rule({
              id: 'r4', name: 'Old mount critical', scope: 'item', host_id: 'h1', item: '/old-mount',
              check_key: 'disk', metric: 'disk.used_pct', severity: 'crit',
            }),
            shadowed: [],
          },
        ],
      },
    });
    renderPanel();

    const expected = `not reported since ${new Date('2026-02-14T08:00:00Z').toLocaleDateString()}`;
    expect(await screen.findByText(expected)).toBeInTheDocument();
    expect(screen.queryByText('no longer observed')).not.toBeInTheDocument();
  });

  it('falls back to the bare label when the item has not been discovered yet, rather than inventing a date', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': {
        effective: [
          {
            check_key: 'disk',
            item: '/old-mount',
            metric: 'disk.used_pct',
            severity: 'crit',
            observed: false,
            winner: rule({
              id: 'r4', name: 'Old mount critical', scope: 'item', host_id: 'h1', item: '/old-mount',
              check_key: 'disk', metric: 'disk.used_pct', severity: 'crit',
            }),
            shadowed: [],
          },
        ],
      },
    });
    renderPanel();

    expect(await screen.findByText('no longer observed')).toBeInTheDocument();
    expect(screen.queryByText(/not reported since/)).not.toBeInTheDocument();
  });

  it('still folds the shadowed rules of an unobserved entry underneath it', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 10, created_at: '', updated_at: '' }] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': {
        effective: [
          {
            check_key: 'disk',
            item: '/old-mount',
            metric: 'disk.used_pct',
            severity: 'crit',
            observed: false,
            winner: rule({ id: 'r4', name: 'Old mount critical', scope: 'item', host_id: 'h1', item: '/old-mount', check_key: 'disk', metric: 'disk.used_pct', severity: 'crit' }),
            shadowed: [rule({ id: 'r5', name: 'Web tier disk warning', scope: 'group', group_id: 'g1', check_key: 'disk', metric: 'disk.used_pct' })],
          },
        ],
      },
    });
    renderPanel();

    await screen.findByText('Old mount critical');
    expect(screen.getByText(/1 shadowed/)).toBeInTheDocument();
    expect(screen.getByText('Web tier disk warning')).toBeInTheDocument();
    expect(screen.getByText('Group Web tier')).toBeInTheDocument();
  });

  it('does not repeat an observed tuple — that is the per-row disclosure on the checks card', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': {
        effective: [
          OBSERVED_ENTRY,
          {
            check_key: 'disk',
            item: '/old-mount',
            metric: 'disk.used_pct',
            severity: 'crit',
            observed: false,
            winner: rule({ id: 'r4', name: 'Old mount critical', scope: 'item', host_id: 'h1', item: '/old-mount', check_key: 'disk', metric: 'disk.used_pct', severity: 'crit' }),
            shadowed: [],
          },
        ],
      },
    });
    renderPanel();

    // The unobserved entry is here...
    expect(await screen.findByText('Old mount critical')).toBeInTheDocument();
    // ...the observed one is not — an operator explains it from the value
    // it decided, not from a second copy sitting in this list.
    expect(screen.queryByText('Host override')).not.toBeInTheDocument();
  });

  it('says nothing extra when every rule on this host still targets something it reports', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      // Only observed entries: the remainder this panel exists for is
      // empty, and it says so rather than showing a blank card.
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [OBSERVED_ENTRY] },
    });
    renderPanel();

    expect(await screen.findByText(/no longer reports/)).toBeInTheDocument();
    expect(screen.queryByText('Host override')).not.toBeInTheDocument();
  });

  it('asks for nothing without checks.read', async () => {
    const fetch = mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
    });
    renderPanel(false);

    expect(await screen.findByText(/visible to members/)).toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('reports a failed read in the panel instead of an empty section', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/items': { items: [] },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': () =>
        new Response(JSON.stringify({ detail: 'effective rules unavailable' }), { status: 500 }),
    });
    renderPanel();

    expect(await screen.findByRole('alert')).toHaveTextContent('effective rules unavailable');
  });
});
