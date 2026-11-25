import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs, request } from './runRequestTestKit.ts';

const SCHED = '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a99';

const current = {
  template_id: 'tpl1',
  template_name: 'deploy-nginx',
  name: 'Nightly',
  mode: 'cron',
  cron: '0 3 * * *',
  description: 'At 03:00',
  timezone: 'Europe/Berlin',
  guards: { overlap: 'skip', catch_up: { enabled: false, window: '' } },
  extra_vars: { release: '1.0', db_password: { $secret: 'set' } },
  paused: true,
  revision: 4,
};

function schedulePicture(over: Record<string, unknown>) {
  return {
    template_id: 'tpl1',
    template_name: 'deploy-nginx',
    template_revision: 7,
    limit: '',
    playbook: { id: '', source: '' },
    targets: [],
    vault_identities: [],
    extra_vars: {},
    vars_digest: '',
    repo: undefined,
    schedule: { schedule_id: SCHED, ...over },
  };
}

function renderSchedule(
  schedule: Record<string, unknown>,
  role = 'operator',
  requestOver: Record<string, unknown> = {},
  extra: Record<string, unknown> = {},
  lang?: 'de',
) {
  const d = detail({
    request: { kind: 'schedule', target_id: SCHED, ...requestOver },
  });
  mockApi({
    'GET /api/v1/orgs/acme': orgAs(role),
    'GET /api/v1/orgs/acme/members': MEMBERS,
    [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: {
      ...d,
      picture: schedulePicture(schedule),
    },
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

describe('RunRequestDetailPage schedule requests (spec 2026-11-25 C)', () => {
  it('shows a change next to the schedule as it is, and no run sections', async () => {
    renderSchedule({
      action: 'update',
      current,
      new: {
        ...current,
        cron: '*/30 * * * *',
        description: 'Every 30 minutes',
        revision: undefined,
      },
    });
    expect(await screen.findByRole('heading', { name: 'Change a schedule' })).toBeInTheDocument();
    const section = screen.getByRole('region', { name: 'Schedule' });
    const table = within(section).getByRole('table');
    const timetable = within(table).getByRole('row', { name: /Timetable/ });
    expect(within(timetable).getByText(/At 03:00/)).toBeInTheDocument();
    expect(within(timetable).getByText(/Every 30 minutes/)).toBeInTheDocument();
    expect(within(timetable).getByText('changes')).toBeInTheDocument();
    const name = within(table).getByRole('row', { name: /Name/ });
    expect(within(name).queryByText('changes')).not.toBeInTheDocument();
    // Secrets stay masked.
    expect(within(table).getAllByText(/db_password: secret \(hidden\)/).length).toBeGreaterThan(0);
    expect(within(section).getByText(/A paused schedule stays paused/)).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Targets' })).not.toBeInTheDocument();
  });

  it('says the assistant set the limit and extra vars', async () => {
    renderSchedule({
      action: 'create',
      assistant_extra_vars: true,
      new: { ...current, paused: false, revision: undefined },
    });
    expect(await screen.findByRole('heading', { name: 'Create a schedule' })).toBeInTheDocument();
    expect(screen.getByText(/take every text in them literally/)).toBeInTheDocument();
  });

  it('names only the parts the assistant set', async () => {
    renderSchedule({ action: 'update', current, assistant_limit: true, new: { ...current, limit: 'web-*', revision: undefined } });
    expect(await screen.findByText(/The limit comes from the assistant/)).toBeInTheDocument();
    expect(screen.queryByText(/The extra vars come from the assistant/)).not.toBeInTheDocument();
  });

  it('says the template carries open change reviews and links them', async () => {
    renderSchedule({ action: 'resume', current, open_change_reviews: ['0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a01'] });
    const note = await screen.findByRole('note', { name: /unreviewed changes/ });
    expect(within(note).getByRole('link', { name: 'Open the review' })).toHaveAttribute('href', '/o/acme/runs/requests/0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a01');
  });

  it('shows the template revision and says when the template changed since the request', async () => {
    renderSchedule({ action: 'resume', current }, 'operator', { template_revision: 6 });
    expect(await screen.findByText('Template revision 7')).toBeInTheDocument();
    expect(screen.getByText('The template changed since the request (it was at revision 6).')).toBeInTheDocument();
  });

  it('says nothing about a template change when there is none', async () => {
    renderSchedule({ action: 'resume', current }, 'operator', { template_revision: 7 });
    expect(await screen.findByText('Template revision 7')).toBeInTheDocument();
    expect(screen.queryByText(/changed since the request/)).not.toBeInTheDocument();
  });

  it('approves with the fingerprint and opens the schedule', async () => {
    const user = userEvent.setup();
    let approved: unknown;
    const router = renderSchedule(
      { action: 'resume', current },
      'operator',
      {},
      {
        [`POST /api/v1/orgs/acme/run-requests/${RQ}/approve`]: (req: MockRequest) => {
          approved = req.body;
          return {
            request: request({
              kind: 'schedule',
              status: 'approved',
              target_id: SCHED,
            }),
          };
        },
      },
    );
    expect(await screen.findByRole('heading', { name: 'Resume a schedule' })).toBeInTheDocument();
    const state = within(screen.getByRole('table')).getByRole('row', {
      name: /State/,
    });
    expect(within(state).getByText('paused')).toBeInTheDocument();
    expect(within(state).getByText('active')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(approved).toEqual({ fingerprint: 'fp-1' }));
    expect(await screen.findByText('SCHEDULES PAGE')).toBeInTheDocument();
    expect(router.state.location.search).toMatchObject({ id: SCHED });
  });

  it('holds the approval of a schedule that is gone', async () => {
    renderSchedule({ action: 'update', gone: true, new: current });
    expect(await screen.findByText(/no longer exists/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Reject' })).toBeEnabled();
  });

  it('offers no decision to a viewer', async () => {
    renderSchedule({ action: 'create', new: current }, 'viewer');
    expect(await screen.findByRole('heading', { name: 'Create a schedule' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
    expect(screen.getByText('Operators, admins and owners can approve or reject schedule requests.')).toBeInTheDocument();
  });

  it('says what an approval did', async () => {
    renderSchedule({ action: 'create', new: current }, 'operator', {
      status: 'approved',
      decided_at: '2026-11-16T09:05:00Z',
    });
    expect(await screen.findByText('Approved. The schedule was saved.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open the schedules' })).toBeInTheDocument();
  });

  it('speaks German', async () => {
    renderSchedule({ action: 'update', current, new: { ...current, name: 'Nachts' } }, 'operator', {}, {}, 'de');
    expect(await screen.findByRole('heading', { name: 'Zeitplan ändern' })).toBeInTheDocument();
    expect(within(screen.getByRole('table')).getByText('ändert sich')).toBeInTheDocument();
  });
});
