import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs, request } from './runRequestTestKit.ts';

const SCHED = '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a77';

const schedule = {
  template_id: 'tpl1',
  template_name: 'deploy-nginx',
  name: 'Nightly',
  mode: 'cron',
  cron: '0 3 * * *',
  description: 'At 03:00',
  timezone: 'Europe/Berlin',
  guards: { overlap: 'skip', catch_up: { enabled: false, window: '' } },
  paused: true,
  revision: 5,
};

const changes = [
  {
    subject: 'job_template',
    id: 'tpl1',
    name: 'deploy-nginx',
    user_id: 'u1',
    token_id: 'tok2',
    fields: [
      { field: 'limit', old: null, new: 'web*' },
      { field: 'extra_vars.db_password', old: { $secret: 'set' }, new: { $secret: 'set' } },
      { field: 'scope', old: 'all managed hosts', new: 'hosts web-1' },
    ],
  },
];

function reviewPicture(review: Record<string, unknown>) {
  return {
    template_id: 'tpl1',
    template_name: 'deploy-nginx',
    template_revision: 8,
    limit: '',
    playbook: { id: '', source: '' },
    targets: [],
    vault_identities: [],
    extra_vars: {},
    vars_digest: '',
    repo: undefined,
    review: { item_id: SCHED, changes, paused_for_review: true, ...review },
  };
}

function renderReview(review: Record<string, unknown>, role = 'operator', kind = 'schedule_review', extra: Record<string, unknown> = {}, lang?: 'de') {
  const d = detail({ request: { kind, target_id: SCHED, reason: 'An assistant changed job template deploy-nginx' } });
  mockApi({
    'GET /api/v1/orgs/acme': orgAs(role),
    'GET /api/v1/orgs/acme/members': MEMBERS,
    [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: { ...d, picture: reviewPicture(review) },
    ...extra,
  });
  return renderRoutes(
    [
      { path: '/runs/requests/$id', component: RunRequestDetailPage },
      { path: '/schedules', component: () => <p>SCHEDULES PAGE</p> },
    ],
    `/o/acme/runs/requests/${RQ}`,
    new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    lang,
  );
}

describe('RunRequestDetailPage reviews of an assistant change (spec 2026-11-25 C2)', () => {
  it('shows the paused schedule and what the assistant changed, secrets masked', async () => {
    renderReview({ item: 'schedule', schedule });
    expect(await screen.findByRole('heading', { name: 'Review a paused schedule' })).toBeInTheDocument();
    const section = screen.getByRole('region', { name: 'Review' });
    expect(within(section).getByText(/An assistant changed what this schedule runs/)).toBeInTheDocument();
    expect(within(section).getByText('Nightly')).toBeInTheDocument();
    const table = within(section).getByRole('table', { name: /Job template deploy-nginx/ });
    const limit = within(table).getByRole('row', { name: /limit/ });
    expect(within(limit).getByText('—')).toBeInTheDocument();
    expect(within(limit).getByText('web*')).toBeInTheDocument();
    const secret = within(table).getByRole('row', { name: /extra_vars.db_password/ });
    expect(within(secret).getAllByText('secret (hidden)')).toHaveLength(2);
    expect(within(section).getByText(/claude-ops/)).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Targets' })).not.toBeInTheDocument();
  });

  it('approves with the fingerprint and opens the schedules', async () => {
    const user = userEvent.setup();
    let approved: unknown;
    const router = renderReview({ item: 'schedule', schedule }, 'operator', 'schedule_review', {
      [`POST /api/v1/orgs/acme/run-requests/${RQ}/approve`]: (req: MockRequest) => {
        approved = req.body;
        return { request: request({ kind: 'schedule_review', status: 'approved', target_id: SCHED }) };
      },
    });
    await user.click(await screen.findByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(approved).toEqual({ fingerprint: 'fp-1' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/schedules'));
  });

  it('says when the item is no longer paused for the review, and when it is gone', async () => {
    renderReview({ item: 'schedule', schedule: { ...schedule, paused: false }, paused_for_review: false });
    expect(await screen.findByText(/no longer paused for this review/)).toBeInTheDocument();
  });

  it('cannot approve a gone item', async () => {
    renderReview({ item: 'schedule', gone: true });
    expect(await screen.findByText(/no longer exists/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
  });

  it('shows a disabled repository sync trigger and needs repos.write to approve', async () => {
    const trigger = {
      kind: 'repo_sync', repo_id: 'r1', repo_name: 'infra', repo_url: 'https://git.example.com/infra.git', repo_branch: 'main',
      name: 'push', enabled: false, preset: 'github', signature_header: 'X-Hub-Signature-256', allow_cidrs: [], mapping: {},
      on_active: 'skip', rate_limit: { count: 5, per: '1m' }, revision: 3,
    };
    renderReview({ item: 'trigger', trigger }, 'viewer', 'trigger_review');
    expect(await screen.findByRole('heading', { name: 'Review a disabled webhook trigger' })).toBeInTheDocument();
    expect(screen.getByText(/syncs infra/)).toBeInTheDocument();
    expect(screen.getByText(/can approve or reject reviews/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    renderReview({ item: 'schedule', schedule }, 'operator', 'schedule_review', {}, 'de');
    expect(await screen.findByRole('heading', { name: 'Pausierten Zeitplan prüfen' })).toBeInTheDocument();
  });
});
