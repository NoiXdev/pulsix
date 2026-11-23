import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { waitFor, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { FakeEventSource } from '../runs/runPageTestKit.tsx';
import { dryRoutes, form } from './dryRunTestKit.ts';
import { renderAt, template } from './playbookPageTestKit.tsx';

/* Which hosts the Dry run form offers: the template's scope read in the
 * browser, narrowed by its limit, in few requests. */

const member = (group: string, host: string, name: string) => ({ group_id: group, group_name: group, host_id: host, host_name: name, complete: true, orphaned: [], problems: [], revision: 1, vars: {} });

/** A route that answers after a tick and counts how many are in flight. */
function tracked(answer: (id: string) => unknown) {
  const stats = { live: 0, peak: 0, calls: 0 };
  const route = (req: { path: string }) => {
    stats.live++;
    stats.calls++;
    stats.peak = Math.max(stats.peak, stats.live);
    const id = req.path.split('/').filter((p) => p !== 'members').pop() as string;
    return new Promise((resolve) =>
      setTimeout(() => {
        stats.live--;
        resolve(answer(id));
      }, 5),
    );
  };
  return { stats, route };
}

describe('PlaybookPage Dry run tab, the hosts offered', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("applies the template's limit to the offered hosts", async () => {
    const groups = { groups: [{ id: 'g1', name: 'all' }] };
    mockApi(
      dryRoutes('operator', {
        extra: {
          'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1', { limit: 'web-*:!web-03', scope: { group_id: 'g1', host_ids: [] } })] },
          'GET /api/v1/orgs/acme/inventory/groups': groups,
          'GET /api/v1/orgs/acme/inventory/groups/g1/members': { members: [member('g1', 'h1', 'web-01'), member('g1', 'h3', 'web-03'), member('g1', 'h4', 'db-01')] },
        },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await within(f).findByRole('checkbox', { name: 'web-01' });
    expect(within(f).queryByRole('checkbox', { name: 'web-03' })).not.toBeInTheDocument();
    expect(within(f).queryByRole('checkbox', { name: 'db-01' })).not.toBeInTheDocument();
  });

  it('keeps every host when the limit cannot be decided in the browser', async () => {
    mockApi(
      dryRoutes('operator', {
        extra: {
          'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1', { limit: '~web.*', scope: { group_id: null, host_ids: ['h2'] } })] },
          'GET /api/v1/orgs/acme/hosts/h2': { host: { id: 'h2', name: 'db-02' } },
        },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    expect(await within(await form()).findByRole('checkbox', { name: 'db-02' })).toBeInTheDocument();
  });

  it('reads explicit hosts under a limit before it says the scope holds none', async () => {
    let answer: (v: unknown) => void = () => {};
    const named = new Promise((resolve) => (answer = resolve));
    mockApi(
      dryRoutes('operator', {
        extra: {
          'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1', { limit: 'web-*', scope: { group_id: null, host_ids: ['h1'] } })] },
          'GET /api/v1/orgs/acme/hosts/h1': () => named,
        },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    expect(await within(f).findByText("Reading the template's hosts…")).toBeInTheDocument();
    expect(within(f).queryByText("The template's scope holds no hosts.")).not.toBeInTheDocument();
    answer({ host: { id: 'h1', name: 'web-01' } });
    expect(await within(f).findByRole('checkbox', { name: 'web-01' })).toBeInTheDocument();
    expect(within(f).queryByText("The template's scope holds no hosts.")).not.toBeInTheDocument();
  });

  it('names explicit hosts with a bounded number of requests at once', async () => {
    const ids = Array.from({ length: 12 }, (_, i) => `h${i + 1}`);
    const names = tracked((id) => ({ host: { id, name: `web-${id.slice(1).padStart(2, '0')}` } }));
    mockApi(
      dryRoutes('operator', {
        extra: {
          'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1', { scope: { group_id: null, host_ids: ids } })] },
          ...Object.fromEntries(ids.map((id) => [`GET /api/v1/orgs/acme/hosts/${id}`, names.route])),
        },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await within(f).findByRole('checkbox', { name: 'web-12' });
    expect(within(f).getAllByRole('checkbox', { name: /^web-/ })).toHaveLength(12);
    expect(names.stats.calls).toBe(12);
    expect(names.stats.peak).toBeLessThanOrEqual(4);
  });

  it("reads a template group's subgroups with a bounded number of requests at once", async () => {
    const gs = Array.from({ length: 10 }, (_, i) => `g${i + 1}`);
    const members = tracked((id) => ({ members: [member(id, `h-${id}`, `host-${id}`)] }));
    mockApi(
      dryRoutes('operator', {
        extra: {
          'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1', { scope: { group_id: 'g1', host_ids: [] } })] },
          'GET /api/v1/orgs/acme/inventory/groups': { groups: gs.map((g, i) => ({ id: g, name: g, ...(i > 0 ? { parent_id: 'g1' } : {}) })) },
          ...Object.fromEntries(gs.map((g) => [`GET /api/v1/orgs/acme/inventory/groups/${g}/members`, members.route])),
        },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await within(f).findByRole('checkbox', { name: 'host-g10' });
    expect(members.stats.calls).toBe(10);
    expect(members.stats.peak).toBeLessThanOrEqual(4);
  });

  it('still falls back to every managed host when a read fails', async () => {
    mockApi(
      dryRoutes('operator', {
        extra: {
          'GET /api/v1/orgs/acme/job-templates': { templates: [template('t1', 'Deploy web', 'p1', { scope: { group_id: 'g1', host_ids: [] } })] },
          'GET /api/v1/orgs/acme/inventory/groups': () => new Response('{"status":500}', { status: 500, headers: { 'Content-Type': 'application/json' } }),
        },
      }).routes,
    );
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await waitFor(() => expect(within(f).getByRole('searchbox')).toBeInTheDocument());
  });
});
