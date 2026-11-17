import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestsPage } from './RunRequestsPage.tsx';
import { inMinutes, request } from './runRequestTestKit.ts';

function renderList() {
  return renderRoutes(
    [
      { path: '/runs/requests', component: RunRequestsPage },
      { path: '/runs/requests/$id', component: () => <div>REQUEST DETAIL</div> },
    ],
    '/o/acme/runs/requests',
  );
}

describe('RunRequestsPage', () => {
  it('lists pending requests first with their expiry, then the rest with their status', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/job-templates': { templates: [{ id: 'tpl1', name: 'deploy-nginx' }] },
      'GET /api/v1/orgs/acme/run-requests': {
        requests: [
          request({ id: 'a', reason: 'Old one', status: 'approved', created_at: '2026-11-15T08:00:00Z', decided_at: '2026-11-15T08:05:00Z' }),
          request({ id: 'b', reason: 'Nobody looked', status: 'expired', created_at: '2026-11-15T09:00:00Z' }),
          request({ id: 'c', reason: 'Roll out nginx', status: 'pending', template_id: 'tpl1', expires_at: inMinutes(30) }),
          request({ id: 'd', reason: 'Retry the failed hosts', kind: 'retry_failed', status: 'pending', template_id: 'tpl9', expires_at: inMinutes(90) }),
        ],
      },
    });
    renderList();

    await screen.findByRole('link', { name: 'Roll out nginx' });
    const rows = Array.from(document.querySelectorAll<HTMLElement>('[data-list-row]'));
    expect(rows.map((r) => r.querySelector('a')?.textContent)).toEqual(['Roll out nginx', 'Retry the failed hosts', 'Nobody looked', 'Old one']);
    expect(within(rows[0]).getByText('deploy-nginx')).toBeInTheDocument();
    expect(within(rows[0]).getByText('pending')).toBeInTheDocument();
    expect(within(rows[0]).getByText('expires in 30 minutes')).toBeInTheDocument();
    // A template the list does not know is named by a piece of its id.
    expect(within(rows[1]).getByText('tpl9')).toBeInTheDocument();
    expect(within(rows[1]).getByText('retry of failed hosts')).toBeInTheDocument();
    expect(within(rows[2]).getByText('expired')).toBeInTheDocument();
    expect(within(rows[3]).getByText('approved')).toBeInTheDocument();
    expect(screen.getByText('2 pending')).toBeInTheDocument();
  });

  it('says so when there are none', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/job-templates': { templates: [] },
      'GET /api/v1/orgs/acme/run-requests': { requests: [] },
    });
    renderList();
    expect(await screen.findByText('No run requests yet.')).toBeInTheDocument();
  });
});
