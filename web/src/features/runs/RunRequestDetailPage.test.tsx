import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs, request } from './runRequestTestKit.ts';

function renderDetail() {
  return renderRoutes(
    [
      { path: '/runs', component: () => <div>RUNS LIST</div> },
      { path: '/runs/$id', component: () => <div>RUN PAGE</div> },
      { path: '/runs/requests/$id', component: RunRequestDetailPage },
    ],
    `/o/acme/runs/requests/${RQ}`,
  );
}

const DETAIL_PATH = `GET /api/v1/orgs/acme/run-requests/${RQ}`;
const APPROVE_PATH = `POST /api/v1/orgs/acme/run-requests/${RQ}/approve`;
const REJECT_PATH = `POST /api/v1/orgs/acme/run-requests/${RQ}/reject`;

function conflict(detailText: string) {
  return new Response(JSON.stringify({ title: 'Conflict', status: 409, detail: detailText }), {
    status: 409,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('RunRequestDetailPage', () => {
  it('shows the whole run picture in sections, secrets masked', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': orgAs('viewer'), 'GET /api/v1/orgs/acme/members': MEMBERS, [DETAIL_PATH]: detail() });
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Run request' })).toBeInTheDocument();
    expect(screen.getByText('Roll out the new nginx config')).toBeInTheDocument();
    expect(await screen.findByText('Tim R.')).toBeInTheDocument();

    const template = screen.getByRole('region', { name: 'Template' });
    expect(within(template).getByText('deploy-nginx')).toBeInTheDocument();
    expect(within(template).getByText('7')).toBeInTheDocument();
    expect(within(template).getByText('web')).toBeInTheDocument();

    const playbook = screen.getByRole('region', { name: 'Playbook and repository' });
    expect(within(playbook).getByText('site/nginx.yml')).toBeInTheDocument();
    expect(within(playbook).getByText('infra')).toBeInTheDocument();
    expect(within(playbook).getByText('https://git.example.com/infra.git')).toBeInTheDocument();
    expect(within(playbook).getByText('main')).toBeInTheDocument();
    expect(within(playbook).getByText('ansible')).toBeInTheDocument();

    const targets = screen.getByRole('region', { name: 'Targets' });
    const rows = within(targets).getAllByRole('row');
    expect(rows).toHaveLength(3);
    expect(within(rows[1]).getByText('web-01')).toBeInTheDocument();
    expect(within(rows[1]).getByText('10.0.0.11')).toBeInTheDocument();
    expect(within(rows[1]).getByText('bastion')).toBeInTheDocument();
    expect(within(rows[2]).getByText('2222')).toBeInTheDocument();

    const credentials = screen.getByRole('region', { name: 'Credentials' });
    expect(within(credentials).getByText('deploy-key')).toBeInTheDocument();
    expect(within(credentials).getByText('prod')).toBeInTheDocument();
    expect(within(credentials).getByText('vault-prod')).toBeInTheDocument();

    const vars = screen.getByRole('region', { name: 'Extra vars' });
    expect(within(vars).getByText('nginx_workers')).toBeInTheDocument();
    expect(within(vars).getByText('4')).toBeInTheDocument();
    expect(within(vars).getByText('api_password')).toBeInTheDocument();
    expect(within(vars).getByText('secret (hidden)')).toBeInTheDocument();
    expect(vars).not.toHaveTextContent('$secret');

    expect(screen.queryByRole('region', { name: 'Retry' })).not.toBeInTheDocument();
  });

  it('shows the job and the failed hosts of a retry request', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('viewer'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: detail({
        request: { kind: 'retry_failed', job_id: 'job12345-0000-0000-0000-000000000000' },
        picture: { retry: { job_id: 'job12345-0000-0000-0000-000000000000', failed_hosts: ['web-02'] } },
      }),
    });
    renderDetail();
    const retry = await screen.findByRole('region', { name: 'Retry' });
    expect(within(retry).getByRole('link', { name: 'job12345' })).toBeInTheDocument();
    expect(within(retry).getByText('web-02')).toBeInTheDocument();
  });

  it('offers no decision to a viewer', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': orgAs('viewer'), 'GET /api/v1/orgs/acme/members': MEMBERS, [DETAIL_PATH]: detail() });
    renderDetail();
    expect(await screen.findByRole('heading', { name: 'Run request' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reject' })).not.toBeInTheDocument();
    expect(screen.getByText('Operators, admins and owners can approve or reject run requests.')).toBeInTheDocument();
  });

  it('approves with the fingerprint of the loaded picture and opens the run', async () => {
    const user = userEvent.setup();
    let approved: unknown;
    let groupQuery: string | null = null;
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: detail({ fingerprint: 'fp-loaded' }),
      [APPROVE_PATH]: (req: MockRequest) => {
        approved = req.body;
        return { request: request({ status: 'approved', run_group_id: 'grp1' }) };
      },
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        groupQuery = req.search.get('group');
        return { jobs: [{ id: 'job1' }], next_cursor: '' };
      },
    });
    const router = renderDetail();

    await user.click(await screen.findByRole('button', { name: 'Approve' }));
    expect(await screen.findByText('RUN PAGE')).toBeInTheDocument();
    expect(approved).toEqual({ fingerprint: 'fp-loaded' });
    expect(groupQuery).toBe('grp1');
    expect(router.state.location.pathname).toBe('/o/acme/runs/job1');
  });

  it('reloads the picture and asks for another look when it changed since the page loaded', async () => {
    const user = userEvent.setup();
    let loads = 0;
    const sent: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('admin'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: () => {
        loads += 1;
        return loads === 1
          ? detail({ fingerprint: 'fp-old' })
          : detail({ fingerprint: 'fp-new', picture: { template_name: 'deploy-nginx-v2' } });
      },
      [APPROVE_PATH]: (req: MockRequest) => {
        sent.push(req.body);
        return conflict('changed since you looked at it; review it again');
      },
    });
    renderDetail();

    await user.click(await screen.findByRole('button', { name: 'Approve' }));
    expect(await screen.findByText('This request changed since you opened it. Review it again.')).toBeInTheDocument();
    expect(await within(screen.getByRole('region', { name: 'Template' })).findByText('deploy-nginx-v2')).toBeInTheDocument();
    expect(loads).toBe(2);

    // The second approval sends the fingerprint of what is shown now.
    await user.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(sent).toEqual([{ fingerprint: 'fp-old' }, { fingerprint: 'fp-new' }]));
  });

  it('shows the server answer when the request expired', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('owner'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: detail(),
      [APPROVE_PATH]: () => conflict('the request expired'),
    });
    renderDetail();
    await user.click(await screen.findByRole('button', { name: 'Approve' }));
    expect(await screen.findByRole('status')).toHaveTextContent('the request expired');
  });

  it('rejects after confirming', async () => {
    const user = userEvent.setup();
    let rejected = false;
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: () => detail({ request: { status: rejected ? 'rejected' : 'pending' } }),
      [REJECT_PATH]: () => {
        rejected = true;
        return { request: request({ status: 'rejected' }) };
      },
    });
    renderDetail();
    await user.click(await screen.findByRole('button', { name: 'Reject' }));
    await acceptConfirm(user);
    await waitFor(() => expect(rejected).toBe(true));
    expect(await screen.findByText('Rejected. The run was not started.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
  });
  describe('follow-ups', () => {
    const FOLLOW_UPS = {
      start: true,
      templates: [
        { template_id: 'f1', template_name: 'reload-lb', template_revision: 3 },
        { template_id: 'f2', template_name: 'warm-cache', template_revision: 9 },
        { template_id: 'f3', template_name: 'old', stop: 'deleted' },
      ],
    };

    it('shows the nav item only when the picture has follow-ups', async () => {
      mockApi({ 'GET /api/v1/orgs/acme': orgAs('viewer'), 'GET /api/v1/orgs/acme/members': MEMBERS, [DETAIL_PATH]: detail() });
      renderDetail();
      expect(await screen.findByRole('heading', { name: 'Run request' })).toBeInTheDocument();
      expect(screen.queryByRole('region', { name: 'Follow-ups' })).not.toBeInTheDocument();
      expect(screen.queryByRole('link', { name: /Follow-ups/ })).not.toBeInTheDocument();
    });

    it('lists them in a section with a nav item', async () => {
      mockApi({
        'GET /api/v1/orgs/acme': orgAs('viewer'),
        'GET /api/v1/orgs/acme/members': MEMBERS,
        [DETAIL_PATH]: detail({ picture: { follow_ups: FOLLOW_UPS } }),
      });
      renderDetail();
      const region = await screen.findByRole('region', { name: 'Follow-ups' });
      expect(within(region).getByText('reload-lb')).toBeInTheDocument();
      // A viewer cannot decide, so the boxes are inert.
      expect(within(region).getByRole('checkbox', { name: 'reload-lb' })).toBeDisabled();
      expect(screen.getAllByText('Follow-ups').length).toBeGreaterThan(1);
    });

    it('sends no follow_ups for a plain approval', async () => {
      const user = userEvent.setup();
      let approved: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': orgAs('operator'),
        'GET /api/v1/orgs/acme/members': MEMBERS,
        [DETAIL_PATH]: detail({ fingerprint: 'fp-a', picture: { follow_ups: FOLLOW_UPS } }),
        [APPROVE_PATH]: (req: MockRequest) => {
          approved = req.body;
          return { request: request({ status: 'approved' }) };
        },
      });
      renderDetail();
      await user.click(await screen.findByRole('button', { name: 'Approve' }));
      await waitFor(() => expect(approved).toEqual({ fingerprint: 'fp-a' }));
      expect(approved).not.toHaveProperty('follow_ups');
    });

    it('sends the rest when one follow-up is unchecked', async () => {
      const user = userEvent.setup();
      let approved: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': orgAs('operator'),
        'GET /api/v1/orgs/acme/members': MEMBERS,
        [DETAIL_PATH]: detail({ fingerprint: 'fp-a', picture: { follow_ups: FOLLOW_UPS } }),
        [APPROVE_PATH]: (req: MockRequest) => {
          approved = req.body;
          return { request: request({ status: 'approved' }) };
        },
      });
      renderDetail();
      await user.click(await screen.findByRole('checkbox', { name: 'reload-lb' }));
      await user.click(screen.getByRole('button', { name: 'Approve' }));
      await waitFor(() => expect(approved).toEqual({ fingerprint: 'fp-a', follow_ups: ['f2'] }));
    });

    it('sends an empty list when all are unchecked', async () => {
      const user = userEvent.setup();
      let approved: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': orgAs('operator'),
        'GET /api/v1/orgs/acme/members': MEMBERS,
        [DETAIL_PATH]: detail({ fingerprint: 'fp-a', picture: { follow_ups: FOLLOW_UPS } }),
        [APPROVE_PATH]: (req: MockRequest) => {
          approved = req.body;
          return { request: request({ status: 'approved' }) };
        },
      });
      renderDetail();
      await user.click(await screen.findByRole('checkbox', { name: 'reload-lb' }));
      await user.click(screen.getByRole('checkbox', { name: 'warm-cache' }));
      await user.click(screen.getByRole('button', { name: 'Approve' }));
      await waitFor(() => expect(approved).toEqual({ fingerprint: 'fp-a', follow_ups: [] }));
    });

    it('sends no list in the mode that starts none', async () => {
      const user = userEvent.setup();
      let approved: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': orgAs('operator'),
        'GET /api/v1/orgs/acme/members': MEMBERS,
        [DETAIL_PATH]: detail({
          fingerprint: 'fp-a',
          picture: { follow_ups: { start: false, templates: [{ template_id: 'f1', template_name: 'reload-lb' }] } },
        }),
        [APPROVE_PATH]: (req: MockRequest) => {
          approved = req.body;
          return { request: request({ status: 'approved' }) };
        },
      });
      renderDetail();
      await user.click(await screen.findByRole('button', { name: 'Approve' }));
      await waitFor(() => expect(approved).toEqual({ fingerprint: 'fp-a' }));
    });

    it('resets the selection to all checked after a changed notice', async () => {
      const user = userEvent.setup();
      let loads = 0;
      const sent: unknown[] = [];
      mockApi({
        'GET /api/v1/orgs/acme': orgAs('operator'),
        'GET /api/v1/orgs/acme/members': MEMBERS,
        [DETAIL_PATH]: () => {
          loads += 1;
          return detail({ fingerprint: loads === 1 ? 'fp-old' : 'fp-new', picture: { follow_ups: FOLLOW_UPS } });
        },
        [APPROVE_PATH]: (req: MockRequest) => {
          sent.push(req.body);
          return conflict('changed since you looked at it; review it again');
        },
      });
      renderDetail();
      await user.click(await screen.findByRole('checkbox', { name: 'reload-lb' }));
      await user.click(screen.getByRole('button', { name: 'Approve' }));
      expect(await screen.findByText('This request changed since you opened it. Review it again.')).toBeInTheDocument();
      await waitFor(() => expect(loads).toBe(2));
      await waitFor(() => expect(screen.getByRole('checkbox', { name: 'reload-lb' })).toBeChecked());
      await user.click(screen.getByRole('button', { name: 'Approve' }));
      await waitFor(() => expect(sent).toEqual([{ fingerprint: 'fp-old', follow_ups: ['f2'] }, { fingerprint: 'fp-new' }]));
    });
  });
});
