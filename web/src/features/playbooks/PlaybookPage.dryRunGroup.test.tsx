import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { FakeEventSource } from '../runs/runPageTestKit.tsx';
import { changed, detail, dryRoutes, form, startIn } from './dryRunTestKit.ts';
import { job, renderAt } from './playbookPageTestKit.tsx';

/* A template over hosts in several zones starts one job per zone, all in
 * one run group: the latest dry run is the whole group — counts, changes,
 * hosts and state over every job of it. Also: what the result says when a
 * run ends without changes but did not succeed. */

const ok = (seq: number, host: string, task: string) => ({ seq, attempt: 1, at: '2026-11-22T10:00:00Z', host, task, state: 'ok', payload: { event: 'runner_on_ok' } });

const counts = (h: string, c: Partial<Record<'ok' | 'changed' | 'skipped' | 'failed' | 'unreachable', number>>) => ({
  host_counts: { [h]: { ok: 0, changed: 0, skipped: 0, failed: 0, unreachable: 0, ...c } },
});

/** d1 in zone z1 changes nothing on web-01; d3 in zone z2 would change
 * web-02's config. */
function twoZones(opts: { d1?: Record<string, unknown>; d3?: Record<string, unknown>; d3Status?: string } = {}) {
  const d3Status = opts.d3Status ?? 'succeeded';
  return {
    'GET /api/v1/orgs/acme/jobs/d1': detail('d1', { hosts: ['h1'], stats: counts('h1', { ok: 2 }), group: [{ id: 'd3', status: d3Status, zone_id: 'z2' }], ...opts.d1 }),
    'GET /api/v1/orgs/acme/jobs/d1/events': { events: [ok(1, 'web-01', 'Install package'), ok(2, 'web-01', 'Render nginx config')] },
    'GET /api/v1/orgs/acme/jobs/d3': detail('d3', {
      zone_id: 'z2', run_group_id: 'd1', hosts: ['h2'], status: d3Status, stats: counts('h2', { ok: 1, changed: 1 }), group: [{ id: 'd1', status: 'succeeded', zone_id: 'z1' }], ...opts.d3,
    }),
    'GET /api/v1/orgs/acme/jobs/d3/events': {
      events: [ok(1, 'web-02', 'Install package'), changed(2, 'web-02', 'Render nginx config', { before: 'user www;\n', after: 'user nginx;\n' })],
    },
  };
}

const itemsOf = (list: HTMLElement) => within(list).getAllByRole('listitem').filter((li) => li.parentElement === list);

describe('PlaybookPage Dry run tab, a run over several zones', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("sums the counts and lists the changes of every zone's job", async () => {
    mockApi(dryRoutes('operator', { extra: twoZones() }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    const changes = await within(result).findByRole('list', { name: 'What would change' });
    expect(itemsOf(changes).map((li) => li.querySelector('h4')?.textContent)).toEqual(['Render nginx config · web-02 · would change']);
    expect(within(changes).getByText('+user nginx;')).toBeInTheDocument();
    const tiles = within(result).getByRole('list', { name: 'Counts over all hosts' });
    expect(within(tiles).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['3ok', '1would change', '0skipped', '0failed', '0unreachable']);
    expect(within(result).getByText(/2 hosts · 2 zones/)).toBeInTheDocument();
    expect(within(result).queryByText('No task would change anything.')).not.toBeInTheDocument();
    expect(within(result).getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/o/acme/runs/d1');
  });

  it('stays running while any job of the group runs', async () => {
    mockApi(dryRoutes('operator', { extra: { ...twoZones({ d3Status: 'running', d3: { stats: {} } }), 'GET /api/v1/orgs/acme/jobs/d3/events': { events: [] } } }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    expect(await within(result).findByText('Running: the result follows live.')).toBeInTheDocument();
    await waitFor(() => expect(FakeEventSource.instances.map((e) => e.url)).toContain('/api/v1/orgs/acme/jobs/d3/stream'));
    expect(within(result).queryByText('No task would change anything.')).not.toBeInTheDocument();
    const d3 = FakeEventSource.instances.find((e) => e.url.endsWith('/d3/stream'))!;
    d3.emit('log', changed(1, 'web-02', 'Render nginx config', { before: 'a\n', after: 'b\n' }));
    expect(await within(result).findByText('+b')).toBeInTheDocument();
  });

  it('reads as failed when any job of the group failed, and claims no "nothing would change"', async () => {
    mockApi(
      dryRoutes('operator', {
        extra: { ...twoZones({ d3Status: 'failed', d3: { error: 'zone fra: ssh refused' } }), 'GET /api/v1/orgs/acme/jobs/d3/events': { events: [] } },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    expect(await within(result).findByText(/· failed ·/)).toBeInTheDocument();
    expect(within(result).getByRole('alert')).toHaveTextContent('zone fra: ssh refused');
    expect(within(result).queryByText('No task would change anything.')).not.toBeInTheDocument();
    expect(within(result).getByText('No task reported a change before the run ended.')).toBeInTheDocument();
  });

  it('follows the group of a dry run just started', async () => {
    const user = userEvent.setup();
    mockApi(
      dryRoutes('operator', {
        latest: null,
        extra: {
          ...twoZones(),
          'POST /api/v1/orgs/acme/playbooks/p1/dry-run': (_req: MockRequest) => ({ jobs: [job('d1', { check_mode: true }), job('d3', { check_mode: true, zone_id: 'z2', run_group_id: 'd1' })] }),
        },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    await startIn(await form(), user);
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    const changes = await within(result).findByRole('list', { name: 'What would change' });
    expect(itemsOf(changes).map((li) => li.querySelector('h4')?.textContent)).toEqual(['Render nginx config · web-02 · would change']);
  });
});

describe('PlaybookPage Dry run tab, a run that ends without changes', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const noChanges = (status: string) =>
    dryRoutes('operator', { detailOf: detail('d1', { status, stats: counts('h1', { ok: 1 }) }), extra: { 'GET /api/v1/orgs/acme/jobs/d1/events': { events: [ok(1, 'web-01', 'Install package')] } } }).routes;

  it('says nothing would change when it succeeded', async () => {
    mockApi(noChanges('succeeded'));
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    expect(await within(result).findByText('No task would change anything.')).toBeInTheDocument();
  });

  it.each(['failed', 'cancelled', 'error'])('says only that no change was reported when it %s', async (status) => {
    mockApi(noChanges(status));
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    expect(await within(result).findByText('No task reported a change before the run ended.')).toBeInTheDocument();
    expect(within(result).queryByText('No task would change anything.')).not.toBeInTheDocument();
  });

  it('speaks German about it', async () => {
    mockApi(noChanges('failed'));
    renderAt('/o/acme/playbooks/p1?tab=dry-run', 'de');
    const result = await screen.findByRole('region', { name: 'Letzter Probelauf' });
    expect(await within(result).findByText('Bis zum Ende des Laufs hat kein Task eine Änderung gemeldet.')).toBeInTheDocument();
  });
});
