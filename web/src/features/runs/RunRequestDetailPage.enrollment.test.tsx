import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs } from './runRequestTestKit.ts';

function renderDetail(picture: Record<string, unknown>, request: Record<string, unknown> = {}, lang?: 'de') {
  mockApi({
    'GET /api/v1/orgs/acme': orgAs('operator'),
    'GET /api/v1/orgs/acme/members': MEMBERS,
    [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: detail({ picture, request }),
  });
  return renderRoutes(
    [{ path: '/runs/requests/$id', component: RunRequestDetailPage }],
    `/o/acme/runs/requests/${RQ}`,
    new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    lang,
  );
}

describe('RunRequestDetailPage agent enrollment (spec 2026-11-25 B)', () => {
  it('names the hosts the run enrols agents on', async () => {
    renderDetail({ agent_enrollment: { hosts: ['web-2', 'web-3'], host_ids: ['h2', 'h3'] } });
    const section = await screen.findByRole('region', { name: 'Agent enrollment' });
    expect(within(section).getByText('Will enrol agents on: web-2, web-3')).toBeInTheDocument();
    expect(within(section).getByText(/single-use enrollment token/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled();
  });

  it('says when every target already has an agent', async () => {
    renderDetail({ agent_enrollment: { hosts: [], host_ids: [] } });
    const section = await screen.findByRole('region', { name: 'Agent enrollment' });
    expect(within(section).getByText(/Every target already has an agent/)).toBeInTheDocument();
  });

  it('says a dry run enrols nothing', async () => {
    renderDetail({ check_mode: true, diff: true, agent_enrollment: { hosts: [], host_ids: [], none_reason: 'dry_run' } });
    const section = await screen.findByRole('region', { name: 'Agent enrollment' });
    expect(within(section).getByText(/A dry run enrols no agent/)).toBeInTheDocument();
  });

  it('says an approved request enrolled the hosts its approval stored', async () => {
    renderDetail(
      { agent_enrollment: { hosts: ['web-2'], host_ids: ['h2'] } },
      { status: 'approved', decided_at: '2026-11-16T09:05:00Z', run_group_id: RQ },
    );
    const section = await screen.findByRole('region', { name: 'Agent enrollment' });
    expect(within(section).getByText('Enrols agents on: web-2')).toBeInTheDocument();
  });

  it('says an approval from before the template enrolled agents enrols none', async () => {
    renderDetail(
      { agent_enrollment: { hosts: [], host_ids: [], none_reason: 'approved_before_enrollment' } },
      { status: 'approved', decided_at: '2026-11-16T09:05:00Z', run_group_id: RQ },
    );
    const section = await screen.findByRole('region', { name: 'Agent enrollment' });
    expect(within(section).getByText(/approved before the template enrolled agents/)).toBeInTheDocument();
  });

  it('shows no section for a template without agent enrollment', async () => {
    renderDetail({});
    await screen.findByRole('region', { name: 'Template' });
    expect(screen.queryByRole('region', { name: 'Agent enrollment' })).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    renderDetail({ agent_enrollment: { hosts: ['web-2'], host_ids: ['h2'] } }, {}, 'de');
    const section = await screen.findByRole('region', { name: 'Agent-Registrierung' });
    expect(within(section).getByText('Wird Agents registrieren auf: web-2')).toBeInTheDocument();
  });
});
