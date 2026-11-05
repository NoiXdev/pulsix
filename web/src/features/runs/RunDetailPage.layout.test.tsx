import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { act, screen, waitFor, within } from '@testing-library/react';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { clippedElements } from '../../test/clipped.ts';
import { FakeEventSource, renderRunPage } from './runPageTestKit.tsx';
import { shortId } from './status.ts';

const JOB_ID = '0197c0a2-0000-7000-8000-000000000001';
const SIBLING_ID = '0197c0aa-0000-7000-8000-000000000009';
const ZONE_ID = '0197c0b1-0000-7000-8000-000000000002';
const JOB_PATH = `/api/v1/orgs/acme/jobs/${JOB_ID}`;

const org = (role = 'operator') => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const ZONES = { zones: [{ id: ZONE_ID, name: 'Production', slug: 'prod' }] };

const job = (over: Record<string, unknown> = {}) => ({
  job: {
    id: JOB_ID, zone_id: ZONE_ID, kind: 'run', status: 'succeeded',
    template: { id: 't1', name: 'deploy-site' }, run_group_id: 'g1', trigger: 'manual',
    actor_name: 'Ada', worker_name: 'worker-fra', hosts: [], stats: {},
    started_at: '2026-09-15T08:00:01Z', finished_at: '2026-09-15T08:00:09Z', created_at: '2026-09-15T08:00:00Z',
    group: [],
    ...over,
  },
});

/** A finished failed run with everything the page can show. */
const FULL = job({
  status: 'failed',
  error: '1 host failed',
  stats: { ok_hosts: ['h1'], failed_hosts: ['h2'] },
  group: [{ id: SIBLING_ID, zone_id: ZONE_ID, status: 'failed' }],
  vault_identities: [{ credential_id: 'c3', label: 'production', from_template: false }],
  vault_identities_source: 'recorded',
});

function routes(over: Record<string, MockRoute> = {}): Record<string, MockRoute> {
  return {
    'GET /api/v1/orgs/acme': org(),
    'GET /api/v1/orgs/acme/zones': ZONES,
    [`GET ${JOB_PATH}`]: FULL,
    [`GET ${JOB_PATH}/events`]: { events: [] },
    ...over,
  };
}

const originalEventSource = globalThis.EventSource;
beforeEach(() => {
  globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
});
afterEach(() => {
  globalThis.EventSource = originalEventSource;
  FakeEventSource.reset();
});

const sidebar = () => screen.getByRole('complementary');
const fact = (label: string) => within(sidebar()).getByText(label, { selector: 'dt' }).nextElementSibling as HTMLElement;
const navHrefs = () => within(screen.getByRole('navigation', { name: 'Sections' })).getAllByRole('link').map((a) => a.getAttribute('href'));

describe('RunDetailPage layout', () => {
  it('puts the run in the sidebar and its parts in sections', async () => {
    mockApi(routes());
    renderRunPage(JOB_ID);

    expect(await screen.findByRole('heading', { level: 1, name: shortId(JOB_ID) })).toBeInTheDocument();
    expect(within(sidebar()).getByText('failed')).toBeInTheDocument();
    expect(within(sidebar()).getByText('deploy-site')).toBeInTheDocument();
    await waitFor(() => expect(fact('Zone')).toHaveTextContent('prod'));
    expect(fact('Worker')).toHaveTextContent('worker-fra');
    expect(fact('Triggered by')).toHaveTextContent('Ada');
    expect(fact('Started')).toHaveTextContent('2026-09-15 08:00:01');
    expect(fact('Finished')).toHaveTextContent('2026-09-15 08:00:09');
    expect(fact('Run group')).toHaveTextContent('2 jobs');
    expect(within(sidebar()).getByRole('button', { name: 'Retry failed' })).toBeEnabled();

    expect(navHrefs()).toEqual(['#result', '#group', '#vault', '#log']);
    const nav = screen.getByRole('navigation', { name: 'Sections' });
    expect(within(nav).getByRole('link', { name: /^Run group/ })).toHaveTextContent('2');
    for (const [id, name] of [
      ['result', 'Result'],
      ['group', 'Run group'],
      ['vault', 'Vault identities'],
      ['log', 'Event log'],
    ]) {
      expect(within(nav).getByRole('link', { name: new RegExp(`^${name}`) })).toHaveAttribute('href', `#${id}`);
      expect(screen.getByRole('region', { name })).toHaveAttribute('id', id);
    }
    const regions = screen.getAllByRole('region');
    expect(regions[regions.length - 1]).toHaveAttribute('id', 'log');

    const result = screen.getByRole('region', { name: 'Result' });
    expect(within(result).getByRole('alert')).toHaveTextContent('1 host failed');
    expect(within(result).getByRole('table', { name: 'Per-host results' })).toBeInTheDocument();
    const vault = screen.getByRole('region', { name: 'Vault identities' });
    expect(within(vault).getByText('recorded at claim time')).toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Event log' })).getByText('No events yet.')).toBeInTheDocument();
    // The run list is one click away in the shell nav; the page no longer
    // links back to it from the short id.
    expect(screen.queryByRole('link', { name: shortId(JOB_ID) })).not.toBeInTheDocument();
  });

  it('omits Run group and Vault identities when the run has none', async () => {
    mockApi(routes({ [`GET ${JOB_PATH}`]: job() }));
    renderRunPage(JOB_ID);

    await screen.findByRole('heading', { level: 1, name: shortId(JOB_ID) });
    expect(navHrefs()).toEqual(['#result', '#log']);
    expect(screen.queryByRole('region', { name: 'Run group' })).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Vault identities' })).not.toBeInTheDocument();
    // A finished run without a recap has nothing more coming: no "yet".
    expect(within(screen.getByRole('region', { name: 'Result' })).getByText('No per-host results were recorded.')).toBeInTheDocument();
    expect(fact('Run group')).toHaveTextContent('1 job');
  });

  it('says results are still to come only while the run is live', async () => {
    mockApi(routes({ [`GET ${JOB_PATH}`]: job({ status: 'running', finished_at: undefined }) }));
    renderRunPage(JOB_ID);

    await screen.findByRole('heading', { level: 1, name: shortId(JOB_ID) });
    const result = screen.getByRole('region', { name: 'Result' });
    expect(within(result).getByText('No per-host results yet.')).toBeInTheDocument();
    expect(within(result).queryByText('No per-host results were recorded.')).not.toBeInTheDocument();
  });

  it('falls back to the short zone id when the zone is not listed', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/zones': { zones: [] } }));
    renderRunPage(JOB_ID);

    await screen.findByRole('heading', { level: 1, name: shortId(JOB_ID) });
    await waitFor(() => expect(fact('Zone')).toHaveTextContent(shortId(ZONE_ID)));
  });

  it("follows the stream's status ahead of the poll and drops Cancel when it ends", async () => {
    mockApi(routes({ [`GET ${JOB_PATH}`]: job({ status: 'running', finished_at: undefined }) }));
    renderRunPage(JOB_ID);

    expect(await within(await screen.findByRole('complementary')).findByRole('button', { name: 'Cancel run' })).toBeInTheDocument();
    expect(within(sidebar()).getByText('running')).toBeInTheDocument();
    // The detail poll keeps answering "running": only the stream's own
    // terminal frame can move the sidebar on.
    FakeEventSource.last()?.emit('status', { id: JOB_ID, status: 'succeeded' });
    await waitFor(() => expect(within(sidebar()).getByText('succeeded')).toBeInTheDocument());
    expect(within(sidebar()).queryByText('running')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Cancel run' })).not.toBeInTheDocument();
  });

  it('sibling runs are rows that link to their run', async () => {
    mockApi(routes());
    renderRunPage(JOB_ID);

    const row = await screen.findByRole('link', { name: shortId(SIBLING_ID) });
    expect(row).toHaveAttribute('href', `/o/acme/runs/${SIBLING_ID}`);
    expect(within(screen.getByRole('region', { name: 'Run group' })).getByRole('link', { name: shortId(SIBLING_ID) })).toBe(row);
    expect(row).toHaveTextContent('failed');
    await waitFor(() => expect(row).toHaveTextContent('prod'));
    expect(within(row).queryAllByRole('link')).toEqual([]);
    expect(within(row).queryAllByRole('button')).toEqual([]);
    expect(screen.queryByRole('button', { name: `Actions for ${shortId(SIBLING_ID)}` })).not.toBeInTheDocument();
  });

  it('hides Cancel and Retry from a viewer', async () => {
    let orgRead = false;
    mockApi(
      routes({
        'GET /api/v1/orgs/acme': () => {
          orgRead = true;
          return org('viewer');
        },
        [`GET ${JOB_PATH}`]: job({ status: 'running', finished_at: undefined }),
      }),
    );
    renderRunPage(JOB_ID);
    await screen.findByRole('heading', { level: 1, name: shortId(JOB_ID) });
    await waitFor(() => expect(orgRead).toBe(true));
    await act(async () => {});
    expect(screen.queryByRole('button', { name: 'Cancel run' })).not.toBeInTheDocument();

    FakeEventSource.last()?.emit('status', { id: JOB_ID, status: 'failed' });
    await waitFor(() => expect(within(sidebar()).getByText('failed')).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: 'Retry failed' })).not.toBeInTheDocument();
    expect(screen.queryAllByRole('button')).toEqual([]);
  });

  it('no clipped cards', async () => {
    mockApi(routes());
    renderRunPage(JOB_ID);
    await screen.findByRole('table', { name: 'Per-host results' });
    await waitFor(() => expect(fact('Zone')).toHaveTextContent('prod'));
    expect(clippedElements(document.body)).toEqual([]);
  });
});
