import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { FakeEventSource } from '../runs/runPageTestKit.tsx';
import { dryRoutes, form, startIn } from './dryRunTestKit.ts';
import { job, renderAt, template } from './playbookPageTestKit.tsx';

/* The hosts chosen in the Dry run form are checked against what the template
 * offers for every kind of scope that names hosts, and kept for one that
 * cannot be listed (the server decides). */

const member = (host: string, name: string) => ({ group_id: 'g1', group_name: 'web', host_id: host, host_name: name, complete: true, orphaned: [], problems: [], revision: 1, vars: {} });

type Scope = { group_id: string | null; host_ids: string[] };

function setup(scope: () => Scope, members: () => unknown[] = () => []) {
  const sent: unknown[] = [];
  mockApi(
    dryRoutes('operator', {
      extra: {
        'GET /api/v1/orgs/acme/job-templates': () => ({ templates: [template('t1', 'Deploy web', 'p1', { scope: scope() })] }),
        'GET /api/v1/orgs/acme/inventory/groups': { groups: [{ id: 'g1', name: 'web' }] },
        'GET /api/v1/orgs/acme/inventory/groups/g1/members': () => ({ members: members() }),
        'POST /api/v1/orgs/acme/playbooks/p1/dry-run': (req: MockRequest) => (sent.push(req.body), { jobs: [job('d1')] }),
      },
    }).routes,
  );
  return sent;
}

const both = () => [member('h1', 'web-01'), member('h2', 'web-02')];

describe('PlaybookPage Dry run tab, the chosen hosts', () => {
  beforeEach(() => {
    FakeEventSource.reset();
    vi.stubGlobal('EventSource', FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("drops a chosen host a group scope's members no longer include", async () => {
    const user = userEvent.setup();
    let current = both();
    const sent = setup(() => ({ group_id: 'g1', host_ids: [] }), () => current);
    const { qc } = renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await user.click(await within(f).findByRole('checkbox', { name: 'web-01' }));
    await user.click(within(f).getByRole('checkbox', { name: 'web-02' }));
    current = [member('h2', 'web-02')];
    await qc.invalidateQueries();
    await waitFor(() => expect(within(f).queryByRole('checkbox', { name: 'web-01' })).not.toBeInTheDocument());
    await startIn(f, user);
    await waitFor(() => expect(sent).toEqual([{ template_id: 't1', diff: true, host_ids: ['h2'] }]));
  });

  it('drops a chosen host a group scope narrowed to explicit hosts no longer names', async () => {
    const user = userEvent.setup();
    let ids = ['h1', 'h2'];
    const sent = setup(() => ({ group_id: 'g1', host_ids: ids }), both);
    const { qc } = renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await user.click(await within(f).findByRole('checkbox', { name: 'web-01' }));
    await user.click(within(f).getByRole('checkbox', { name: 'web-02' }));
    ids = ['h2'];
    await qc.invalidateQueries();
    await waitFor(() => expect(within(f).queryByRole('checkbox', { name: 'web-01' })).not.toBeInTheDocument());
    await startIn(f, user);
    await waitFor(() => expect(sent).toEqual([{ template_id: 't1', diff: true, host_ids: ['h2'] }]));
  });

  it('keeps the chosen hosts for a scope that cannot be listed', async () => {
    const user = userEvent.setup();
    const sent = setup(() => ({ group_id: null, host_ids: [] }));
    renderAt('/o/acme/playbooks/p1?tab=dry-run');
    const f = await form();
    await user.click(await within(f).findByRole('checkbox', { name: 'web-01' }));
    await startIn(f, user);
    await waitFor(() => expect(sent).toEqual([{ template_id: 't1', diff: true, host_ids: ['h1'] }]));
  });
});
