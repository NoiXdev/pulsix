import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { FakeEventSource, renderRunPage } from './runPageTestKit.tsx';

const JOB_ID = '0197c0a2-0000-7000-8000-000000000001';
const JOB_PATH = `/api/v1/orgs/acme/jobs/${JOB_ID}`;
const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };

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

describe('RunDetailPage enrolment tokens (spec 2026-11-25 B)', () => {
  it('shows how many tokens the run minted and how many were used', async () => {
    render({ enroll_tokens: { minted: 3, used: 2 } });
    expect(await screen.findByText('Enrolment tokens')).toBeInTheDocument();
    expect(screen.getByText('3 minted, 2 used')).toBeInTheDocument();
  });

  it('shows nothing for a run that minted no token (a dry run, every host had an agent)', async () => {
    render({ enroll_tokens: { minted: 0, used: 0 } });
    await screen.findByText('deploy-site');
    expect(screen.queryByText('Enrolment tokens')).not.toBeInTheDocument();
  });

  it('shows nothing for a run that enrols no agents', async () => {
    render({});
    await screen.findByText('deploy-site');
    expect(screen.queryByText('Enrolment tokens')).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    render({ enroll_tokens: { minted: 1, used: 0 } }, 'de');
    expect(await screen.findByText('Registrierungs-Tokens')).toBeInTheDocument();
    expect(screen.getByText('1 erstellt, 0 verwendet')).toBeInTheDocument();
  });
});
