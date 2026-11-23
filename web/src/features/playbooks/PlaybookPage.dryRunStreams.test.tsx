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

  it("keeps a polled zone's changes on screen when its job ends, never saying nothing would change", async () => {
    const zones = Array.from({ length: MAX_LIVE_STREAMS + 1 }, (_, i) => `d${i + 1}`);
    const polled = zones[zones.length - 1];
    const calls: Record<string, number> = {};
    const extra: Record<string, unknown> = {};
    zones.forEach((id, i) => {
      extra[`GET /api/v1/orgs/acme/jobs/${id}`] = () => {
        calls[id] = (calls[id] ?? 0) + 1;
        const status = calls[id] > 2 ? 'succeeded' : 'running';
        return detail(id, {
          zone_id: `z${i + 1}`, run_group_id: 'd1', hosts: [`h${i + 1}`], status, stats: {},
          group: zones.filter((o) => o !== id).map((o) => ({ id: o, status, zone_id: `z-${o}` })),
        });
      };
      extra[`GET /api/v1/orgs/acme/jobs/${id}/events`] = { events: id === polled ? [changed(1, 'host-x', 'Task x', { before: 'a\n', after: 'b\n' })] : [] };
    });
    mockApi(dryRoutes('operator', { extra }).routes);
    const flashed: string[] = [];
    const watch = new MutationObserver(() => {
      if (document.body.textContent?.includes('No task would change anything.')) flashed.push('seen');
    });
    watch.observe(document.body, { childList: true, subtree: true, characterData: true });
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    await within(result).findByRole('list', { name: 'What would change' });
    await waitFor(() => expect(within(result).getByText(/· succeeded ·/)).toBeInTheDocument(), { timeout: 8000 });
    await new Promise((r) => setTimeout(r, 300));
    watch.disconnect();
    expect(within(result).getAllByRole('heading', { level: 4 })).toHaveLength(1);
    expect(flashed).toEqual([]);
  }, 12_000);

  it('polls a zone\'s log from the last event it has, keeping what it read', async () => {
    const zones = Array.from({ length: MAX_LIVE_STREAMS + 1 }, (_, i) => `d${i + 1}`);
    const polled = zones[zones.length - 1];
    const afters: string[] = [];
    const extra: Record<string, unknown> = {};
    zones.forEach((id, i) => {
      extra[`GET /api/v1/orgs/acme/jobs/${id}`] = detail(id, {
        zone_id: `z${i + 1}`, run_group_id: 'd1', hosts: [`h${i + 1}`], status: 'running', stats: {},
        group: zones.filter((o) => o !== id).map((o) => ({ id: o, status: 'running', zone_id: `z-${o}` })),
      });
      extra[`GET /api/v1/orgs/acme/jobs/${id}/events`] = { events: [] };
    });
    extra[`GET /api/v1/orgs/acme/jobs/${polled}/events`] = (req: { search: URLSearchParams }) => {
      const after = req.search.get('after') ?? '';
      afters.push(after);
      if (after === '0') return { events: [changed(1, 'host-x', 'Task x', { before: 'a\n', after: 'b\n' })] };
      if (after === '1' && afters.filter((a) => a === '1').length === 1) return { events: [changed(2, 'host-x', 'Task y', { before: 'c\n', after: 'd\n' })] };
      return { events: [] };
    };
    mockApi(dryRoutes('operator', { extra }).routes);
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    const changes = await within(result).findByRole('list', { name: 'What would change' });
    await waitFor(() => expect(within(changes).getAllByRole('heading', { level: 4 })).toHaveLength(2), { timeout: 6000 });
    expect(afters.slice(0, 2)).toEqual(['0', '1']);
    expect(within(changes).getByRole('heading', { level: 4, name: /Task x/ })).toBeInTheDocument();
  }, 10_000);

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

  it("never counts the lead's hosts while it says the lead's zone has not reported", async () => {
    const extra = manyZones();
    extra['GET /api/v1/orgs/acme/jobs/d2'] = () => new Promise(() => {});
    extra['GET /api/v1/orgs/acme/jobs/d1'] = detail('d1', { hosts: ['h1', 'h7'], status: 'running', stats: {}, group: [{ id: 'd2', status: 'running', zone_id: 'z2' }] });
    mockApi(dryRoutes('operator', { extra }).routes);
    // Every text the page showed, also one replaced before the observer ran
    // (a text node's earlier value).
    const seen: string[] = [];
    const note = (text: string | null | undefined) => {
      const m = text?.match(/\d+ hosts? so far, \d+ zones? (has|have) not reported yet/);
      if (m) seen.push(m[0]);
    };
    const watch = new MutationObserver((records) => {
      for (const r of records) {
        note(r.oldValue);
        r.addedNodes.forEach((n) => note(n.textContent));
      }
    });
    watch.observe(document.body, { childList: true, subtree: true, characterData: true, characterDataOldValue: true });
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const result = await screen.findByRole('region', { name: 'Latest dry run' });
    await within(result).findByText(/2 hosts so far, 1 zone has not reported yet/);
    watch.disconnect();
    expect([...new Set(seen)]).toEqual(['2 hosts so far, 1 zone has not reported yet']);
  });
});
