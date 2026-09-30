import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
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

const GROUPS = { groups: [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 10, created_at: '', updated_at: '' }] };

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

/** The tuple's own section, anchored on its heading so a rule name that
 * repeats across tuples cannot be matched in the wrong block. */
const block = (heading: string) => within(screen.getByRole('heading', { name: heading }).closest('li') as HTMLElement);

describe('EffectiveRulesPanel', () => {
  it('shows the winning rule with its origin, and folds the rules it shadowed underneath', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': {
        effective: [
          {
            check_key: 'cpu',
            item: '',
            metric: 'cpu.used_pct',
            severity: 'warn',
            observed: true,
            winner: rule({ id: 'r1', name: 'Host override', scope: 'host', host_id: 'h1' }),
            shadowed: [
              rule({ id: 'r2', name: 'Web tier warning', scope: 'group', group_id: 'g1' }),
              rule({ id: 'r3', name: 'Org default warning', scope: 'org' }),
            ],
          },
        ],
      },
    });
    renderPanel();

    await screen.findByRole('heading', { name: 'cpu.used_pct' });
    const section = block('cpu.used_pct');
    expect(section.getByText('Host override')).toBeInTheDocument();
    expect(section.getByText('Host')).toBeInTheDocument();
    // Both shadowed rules are there, each with its own origin — "folded
    // underneath" is a disclosure, not a silent drop.
    expect(section.getByText('Web tier warning')).toBeInTheDocument();
    expect(section.getByText('Group Web tier')).toBeInTheDocument();
    expect(section.getByText('Org default warning')).toBeInTheDocument();
    expect(section.getByText(/2 shadowed/)).toBeInTheDocument();
    // An observed tuple carries no "no longer observed" label.
    expect(section.queryByText('no longer observed')).not.toBeInTheDocument();
  });

  it('labels an item-scoped rule whose item the host no longer reports', async () => {
    mockApi({
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

  it('says so when nothing decides anything on this host yet', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
    });
    renderPanel();

    expect(await screen.findByText(/No rule decides/)).toBeInTheDocument();
  });

  it('asks for nothing without checks.read', async () => {
    const fetch = mockApi({
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': { effective: [] },
    });
    renderPanel(false);

    expect(await screen.findByText(/visible to members/)).toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('reports a failed read in the panel instead of an empty section', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/hosts/h1/effective-rules': () =>
        new Response(JSON.stringify({ detail: 'effective rules unavailable' }), { status: 500 }),
    });
    renderPanel();

    expect(await screen.findByRole('alert')).toHaveTextContent('effective rules unavailable');
  });
});
