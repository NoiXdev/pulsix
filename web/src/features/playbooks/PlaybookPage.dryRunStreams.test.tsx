import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { FakeEventSource } from '../runs/runPageTestKit.tsx';
import { changed, detail, dryRoutes } from './dryRunTestKit.ts';
import { renderAt } from './playbookPageTestKit.tsx';
import { MAX_LIVE_STREAMS } from './DryRunResult.tsx';

/* A dry run over many zones is one job per zone. Each is a different job, so
 * each needs its own log; but only a few follow theirs by a live stream
 * (a browser holds few connections per server), the rest by polling. And
 * the host count does not drop a zone whose job has not been read yet. */

const ZONES = 6;
const ids = Array.from({ length: ZONES }, (_, i) => `d${i + 1}`);

function manyZones() {
  const routes: Record<string, unknown> = {};
  ids.forEach((id, i) => {
    routes[`GET /api/v1/orgs/acme/jobs/${id}`] = detail(id, {
      zone_id: `z${i + 1}`, run_group_id: 'd1', hosts: [`h${i + 1}`], status: 'running', stats: {},
      group: ids.filter((o) => o !== id).map((o) => ({ id: o, status: 'running', zone_id: `z-${o}` })),
    });
    routes[`GET /api/v1/orgs/acme/jobs/${id}/events`] = { events: [changed(1, `host-${i + 1}`, `Task ${i + 1}`, { before: 'a\n', after: 'b\n' })] };
  });
  return routes;
}

describe('PlaybookPage Dry run tab, many zones', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('streams at most MAX_LIVE_STREAMS jobs and follows the others by polling', async () => {
    mockApi(dryRoutes('operator', { extra: manyZones() }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    const changes = await within(result).findByRole('list', { name: 'What would change' });
    await waitFor(() => expect(within(changes).getAllByRole('heading', { level: 4 })).toHaveLength(ZONES));
    const open = FakeEventSource.instances.filter((e) => !e.closed);
    expect(open.length).toBeGreaterThan(0);
    expect(open.length).toBeLessThanOrEqual(MAX_LIVE_STREAMS);
    // One stream per job at most, none twice.
    expect(new Set(FakeEventSource.instances.map((e) => e.url)).size).toBe(FakeEventSource.instances.length);
  });

  it.each([
    [undefined, /2 hosts so far, 1 zone has not reported yet/, 'Latest dry run'],
    ['de' as const, /2 Hosts bisher, 1 Zone hat noch nicht gemeldet/, 'Letzter Probelauf'],
  ])('counts the hosts it has and says how many zones have not reported yet (%s)', async (locale, sentence, region) => {
    const extra = manyZones();
    // The second zone's detail never answers.
    extra['GET /api/v1/orgs/acme/jobs/d2'] = () => new Promise(() => {});
    extra['GET /api/v1/orgs/acme/jobs/d1'] = detail('d1', { hosts: ['h1', 'h7'], status: 'running', stats: {}, group: [{ id: 'd2', status: 'running', zone_id: 'z2' }] });
    mockApi(dryRoutes('operator', { extra }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run', locale);
    const result = await screen.findByRole('region', { name: region });
    expect(await within(result).findByText(sentence)).toBeInTheDocument();
  });
});
