import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { FakeEventSource, renderRunPage } from './runPageTestKit.tsx';

const JOB_ID = '0197c0a2-0000-7000-8000-000000000001';
const JOB_PATH = `/api/v1/orgs/acme/jobs/${JOB_ID}`;
const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const SHA = 'fedcba9876543210fedcba9876543210fedcba98';

const job = (over: Record<string, unknown> = {}) => ({
  job: {
    id: JOB_ID, zone_id: '0197c0b1-0000-7000-8000-000000000002', kind: 'run', status: 'succeeded',
    template: { id: 't1', name: 'deploy-site' }, run_group_id: 'g1', trigger: 'manual', check_mode: false,
    hosts: [], stats: {}, started_at: '2026-09-15T08:00:01Z', finished_at: '2026-09-15T08:00:09Z', created_at: '2026-09-15T08:00:00Z',
    group: [],
    ...over,
  },
});

function render(over: Record<string, unknown>, locale?: 'de') {
  mockApi({
    'GET /api/v1/orgs/acme': ORG,
    [`GET ${JOB_PATH}`]: job(over),
    [`GET ${JOB_PATH}/events`]: { events: [] },
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

describe('RunDetailPage pinned commit (spec 2026-11-25 A)', () => {
  it('shows the commit an approved run runs', async () => {
    render({ pinned_commit: SHA });
    const short = await screen.findByText(SHA.slice(0, 12));
    expect(short).toHaveAttribute('title', SHA);
    expect(screen.getByText('Pinned commit')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy commit id' })).toBeInTheDocument();
  });

  it('says a queued pinned run waits for a worker that can run it', async () => {
    render({ pinned_commit: SHA, status: 'queued', started_at: null, finished_at: null });
    const note = await screen.findByRole('note', { name: 'Pinned commit' });
    expect(note).toHaveTextContent('Waiting for a worker that can run a pinned commit');
  });

  it('says nothing of waiting once a worker took it', async () => {
    render({ pinned_commit: SHA, status: 'running', finished_at: null });
    await screen.findByText(SHA.slice(0, 12));
    expect(screen.queryByText(/Waiting for a worker/)).not.toBeInTheDocument();
  });

  it('shows nothing for a run of the branch tip', async () => {
    render({});
    await screen.findByText('deploy-site');
    expect(screen.queryByText('Pinned commit')).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    render({ pinned_commit: SHA }, 'de');
    expect(await screen.findByText('Festgelegter Commit')).toBeInTheDocument();
  });

  it('speaks German while waiting', async () => {
    render({ pinned_commit: SHA, status: 'queued', started_at: null, finished_at: null }, 'de');
    expect(await screen.findByText(/Warte auf einen Worker, der einen festgelegten Commit ausführen kann/)).toBeInTheDocument();
  });
});
