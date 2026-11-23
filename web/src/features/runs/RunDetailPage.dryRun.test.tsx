import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { FakeEventSource, renderRunPage } from './runPageTestKit.tsx';

const JOB_ID = '0197c0a2-0000-7000-8000-000000000001';
const JOB_PATH = `/api/v1/orgs/acme/jobs/${JOB_ID}`;
const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };

const job = (over: Record<string, unknown> = {}) => ({
  job: {
    id: JOB_ID, zone_id: '0197c0b1-0000-7000-8000-000000000002', kind: 'run', status: 'failed',
    template: { id: 't1', name: 'deploy-site' }, run_group_id: 'g1', trigger: 'manual', check_mode: false,
    hosts: [], stats: {}, started_at: '2026-09-15T08:00:01Z', finished_at: '2026-09-15T08:00:09Z', created_at: '2026-09-15T08:00:00Z',
    group: [],
    ...over,
  },
});

const DRY = { check_mode: true, dry_run: { tags: ['web', 'db'], diff: true, draft: true } };

function render(over: Record<string, unknown>, events: unknown[] = [], locale?: 'de') {
  mockApi({
    'GET /api/v1/orgs/acme': ORG,
    [`GET ${JOB_PATH}`]: job(over),
    [`GET ${JOB_PATH}/events`]: { events },
  });
  renderRunPage(JOB_ID, locale);
}

const originalEventSource = globalThis.EventSource;
beforeEach(() => {
  globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
});
afterEach(() => {
  globalThis.EventSource = originalEventSource;
  FakeEventSource.reset();
});

describe('RunDetailPage dry runs', () => {
  it('says a dry run is one, with its tags, the draft and the differences', async () => {
    render(DRY);
    const banner = await screen.findByRole('note', { name: 'Dry run' });
    expect(banner).toHaveTextContent('changed nothing');
    expect(within(banner).getByText('web')).toBeInTheDocument();
    expect(within(banner).getByText('db')).toBeInTheDocument();
    expect(within(banner).getByText('Unsaved draft')).toBeInTheDocument();
    expect(within(banner).getByText('Differences shown')).toBeInTheDocument();
  });

  it('speaks in the present tense while the dry run is live, in the past once it is over', async () => {
    render({ ...DRY, status: 'running', finished_at: null });
    const live = await screen.findByRole('note', { name: 'Dry run' });
    expect(live).toHaveTextContent('This run runs in check mode');
    expect(live).toHaveTextContent('changes nothing');
    expect(live).not.toHaveTextContent('ran in check mode');
  });

  it('says a dry run of the saved playbook ran every task, without differences', async () => {
    render({ check_mode: true, dry_run: { tags: [], diff: false, draft: false } });
    const banner = await screen.findByRole('note', { name: 'Dry run' });
    expect(within(banner).getByText('All tasks')).toBeInTheDocument();
    expect(within(banner).getByText('Differences not shown')).toBeInTheDocument();
    expect(within(banner).queryByText('Unsaved draft')).not.toBeInTheDocument();
  });

  it('marks a dry run without recorded options all the same', async () => {
    render({ check_mode: true });
    const banner = await screen.findByRole('note', { name: 'Dry run' });
    expect(within(banner).queryByText('All tasks')).not.toBeInTheDocument();
  });

  it('shows no banner and keeps retry on an ordinary run', async () => {
    render({ stats: { failed_hosts: ['h2'] } });
    expect(await screen.findByRole('button', { name: 'Retry failed' })).toBeEnabled();
    expect(screen.queryByRole('note', { name: 'Dry run' })).not.toBeInTheDocument();
  });

  it('offers no retry of a dry run: it would run the hosts for real', async () => {
    render({ ...DRY, stats: { failed_hosts: ['h2'] } });
    await screen.findByRole('note', { name: 'Dry run' });
    expect(screen.queryByRole('button', { name: 'Retry failed' })).not.toBeInTheDocument();
  });

  it('shows the recap counts per host when the run has them', async () => {
    render({ ...DRY, stats: { ok_hosts: ['h1'], changed_hosts: ['h1'], host_counts: { h1: { ok: 5, changed: 2, skipped: 0, failed: 0, unreachable: 0 } } } });
    const table = await screen.findByRole('table', { name: 'Per-host results' });
    const row = within(table).getByText('h1').closest('tr') as HTMLElement;
    const cells = within(row).getAllByRole('cell').map((c) => c.textContent);
    expect(cells).toEqual(['h1', '5', '2', '·', '·', '·']);
  });

  it('shows no counts, and no labels among numbers, for a host the counts do not name', async () => {
    render({
      stats: {
        ok_hosts: ['h1', 'h2'],
        failed_hosts: ['h2'],
        host_counts: { h1: { ok: 5, changed: 0, skipped: 0, failed: 0, unreachable: 0 } },
      },
    });
    const table = await screen.findByRole('table', { name: 'Per-host results' });
    const row = within(table).getByText('h2').closest('tr') as HTMLElement;
    const cells = within(row).getAllByRole('cell').map((c) => c.textContent);
    expect(cells).toEqual(['h2', '●', '·', '●', '·', '·']);
    // The state is still told, to assistive technology.
    expect(within(row).getByRole('img', { name: 'ok' })).toBeInTheDocument();
    expect(within(row).getByRole('img', { name: 'failed' })).toBeInTheDocument();
    expect(within(table).getByText('h1').closest('tr')).toHaveTextContent('5');
  });

  it('shows the host states of an older run without counts', async () => {
    render({ stats: { ok_hosts: ['h1'], changed_hosts: ['h1'] } });
    const table = await screen.findByRole('table', { name: 'Per-host results' });
    const row = within(table).getByText('h1').closest('tr') as HTMLElement;
    const cells = within(row).getAllByRole('cell').map((c) => c.textContent);
    expect(cells).toEqual(['h1', 'ok', 'changed', '·', '·', '·']);
  });

  it('renders the differences a task reported in the event log', async () => {
    render(DRY, [
      { seq: 1, attempt: 1, at: '2026-09-15T08:00:02Z', host: 'h1', task: 'Write config', state: 'changed', payload: { event_data: { res: { diff: { before: 'a\n', after: 'b\n' } } } } },
    ]);
    const log = await screen.findByRole('list', { name: 'Run events' });
    const diff = await within(log).findByRole('figure', { name: 'Differences' });
    expect(within(diff).getByText('-a')).toBeInTheDocument();
    expect(within(diff).getByText('+b')).toBeInTheDocument();
  });

  it('speaks German', async () => {
    render(DRY, [], 'de');
    const banner = await screen.findByRole('note', { name: 'Probelauf' });
    expect(within(banner).getByText('Ungespeicherter Entwurf')).toBeInTheDocument();
  });
});
