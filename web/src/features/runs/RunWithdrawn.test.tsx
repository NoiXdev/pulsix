import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { FakeEventSource, renderRunPage } from './runPageTestKit.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const ID = '0197c0a2-0000-7000-8000-000000000001';
const SIBLING = '0197c0aa-0000-7000-8000-000000000009';
const ZONE = '0197c0b1-0000-7000-8000-000000000002';

const job = {
  id: ID, zone_id: ZONE, kind: 'run', status: 'error', template: { id: 't1', name: 'deploy-site' },
  run_group_id: 'g1', trigger: 'manual', hosts: [], stats: {}, error: 'run request approval withdrawn',
  created_at: '2026-09-15T08:00:00Z', withdrawn_at_claim: true,
  group: [{ id: SIBLING, zone_id: ZONE, status: 'error', withdrawn_at_claim: true }],
};

function mock() {
  globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
  mockApi({
    'GET /api/v1/orgs/acme': ORG,
    [`GET /api/v1/orgs/acme/jobs/${ID}`]: { job },
    [`GET /api/v1/orgs/acme/jobs/${ID}/events`]: { events: [] },
  });
}

describe('a job withdrawn at its claim', () => {
  it('reads as not run in the header and the group section, with the error text kept', async () => {
    mock();
    renderRunPage(ID);
    await screen.findByText('deploy-site');
    expect(within(screen.getByRole('complementary')).getByText('Not run — approval withdrawn')).toBeInTheDocument();
    const sibling = screen.getByRole('link', { name: SIBLING.slice(0, 8) });
    expect(within(sibling).getByText('Not run — approval withdrawn')).toBeInTheDocument();
    expect(screen.queryByText('error')).not.toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('run request approval withdrawn');
  });

  it('reads in German too', async () => {
    mock();
    renderRunPage(ID, 'de');
    await screen.findByText('deploy-site');
    expect(within(screen.getByRole('complementary')).getByText('Nicht gelaufen – Freigabe zurückgenommen')).toBeInTheDocument();
  });
});
