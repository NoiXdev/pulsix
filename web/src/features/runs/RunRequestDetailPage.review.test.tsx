import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs, request } from './runRequestTestKit.ts';

const DETAIL_PATH = `GET /api/v1/orgs/acme/run-requests/${RQ}`;
const APPROVE_PATH = `POST /api/v1/orgs/acme/run-requests/${RQ}/approve`;
const CHANGED_NOTICE = 'This request changed since you opened it. Review it again.';

function renderDetail(qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  renderRoutes([{ path: '/runs/requests/$id', component: RunRequestDetailPage }], `/o/acme/runs/requests/${RQ}`, qc);
  return qc;
}

function requestedBy(): HTMLElement {
  const label = screen.getByText('Requested by');
  return label.parentElement as HTMLElement;
}

describe('RunRequestDetailPage token', () => {
  it('names the token that asked and, under it, its owner', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': orgAs('operator'), 'GET /api/v1/orgs/acme/members': MEMBERS, [DETAIL_PATH]: detail() });
    renderDetail();
    expect(await screen.findByRole('heading', { name: 'Run request' })).toBeInTheDocument();
    const fact = requestedBy();
    expect(within(fact).getByText('claude-ops')).toBeInTheDocument();
    expect(await within(fact).findByText('owned by Tim R.')).toBeInTheDocument();
  });

  it('names the owner alone when the token has no name to show', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: detail({ request: { requested_by_token_name: '', token_state: 'deleted' } }),
    });
    renderDetail();
    expect(await screen.findByRole('heading', { name: 'Run request' })).toBeInTheDocument();
    expect(await within(requestedBy()).findByText('Tim R.')).toBeInTheDocument();
    expect(within(requestedBy()).queryByText(/owned by/)).not.toBeInTheDocument();
  });

  it.each([
    ['revoked', 'The token that asked for this run can no longer ask for runs here. Approving still starts the run.'],
    ['expired', 'The token that asked for this run has expired. Approving still starts the run.'],
    ['deleted', 'The token that asked for this run no longer exists. Approving still starts the run.'],
  ])('warns when the token is %s, and still offers Approve', async (state, warning) => {
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: detail({ request: { token_state: state } }),
    });
    renderDetail();
    expect(await screen.findByText(warning)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled();
  });

  it.each([['active'], [undefined]])('shows no token warning when the state is %s', async (state) => {
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: detail({ request: { token_state: state } }),
    });
    renderDetail();
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeInTheDocument();
    expect(screen.queryByText(/The token that asked for this run/)).not.toBeInTheDocument();
  });

  it('shows the repository\'s git credential when it has one', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('viewer'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: detail({
        picture: {
          repo: { id: 'r1', name: 'infra', url: 'https://git.example.com/infra.git', branch: 'main', subpath: '', git_credential: 'git-deploy', git_credential_id: 'c9' },
        },
      }),
    });
    renderDetail();
    const playbook = await screen.findByRole('region', { name: 'Playbook and repository' });
    expect(within(playbook).getByText('Git credential')).toBeInTheDocument();
    expect(within(playbook).getByText('git-deploy')).toBeInTheDocument();
  });

  it('leaves the git credential out when the repository has none', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': orgAs('viewer'), 'GET /api/v1/orgs/acme/members': MEMBERS, [DETAIL_PATH]: detail() });
    renderDetail();
    const playbook = await screen.findByRole('region', { name: 'Playbook and repository' });
    expect(within(playbook).queryByText('Git credential')).not.toBeInTheDocument();
  });
});

describe('RunRequestDetailPage reviewed version', () => {
  it('holds Approve when a refetch brings another picture until the new version is reviewed', async () => {
    const user = userEvent.setup();
    let version = 1;
    const sent: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: () =>
        version === 1 ? detail({ fingerprint: 'fp-1' }) : detail({ fingerprint: 'fp-2', picture: { template_name: 'deploy-nginx-v2' } }),
      [APPROVE_PATH]: (req: MockRequest) => {
        sent.push(req.body);
        return { request: request({ status: 'approved' }) };
      },
    });
    const qc = renderDetail();
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeEnabled();
    expect(screen.queryByText(CHANGED_NOTICE)).not.toBeInTheDocument();

    // A background refetch (window focus, another tab's decision) brings a new picture.
    version = 2;
    await qc.refetchQueries({ queryKey: ['run-request'] });
    expect(await within(screen.getByRole('region', { name: 'Template' })).findByText('deploy-nginx-v2')).toBeInTheDocument();
    expect(await screen.findByText(CHANGED_NOTICE)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Reject' })).toBeEnabled();

    await user.click(screen.getByRole('button', { name: 'Review the new version' }));
    expect(screen.queryByText(CHANGED_NOTICE)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Review the new version' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(sent).toEqual([{ fingerprint: 'fp-2' }]));
  });

  it('keeps Approve when a refetch brings the same picture', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [DETAIL_PATH]: () => detail({ fingerprint: 'fp-1' }),
    });
    const qc = renderDetail();
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeEnabled();
    await qc.refetchQueries({ queryKey: ['run-request'] });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled());
    expect(screen.queryByText(CHANGED_NOTICE)).not.toBeInTheDocument();
  });
});
