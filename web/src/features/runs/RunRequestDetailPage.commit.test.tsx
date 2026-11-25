import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs } from './runRequestTestKit.ts';

const SHA = '0123456789abcdef0123456789abcdef01234567';

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

describe('RunRequestDetailPage commit (spec 2026-11-25 A)', () => {
  it('shows the commit that runs, short with the full id on hover and copy', async () => {
    renderDetail({ commit: { sha: SHA } });
    const playbook = await screen.findByRole('region', { name: 'Playbook and repository' });
    const short = within(playbook).getByText(SHA.slice(0, 12));
    expect(short).toHaveAttribute('title', SHA);
    expect(within(playbook).getByRole('button', { name: 'Copy commit id' })).toBeInTheDocument();
    expect(within(playbook).getByText(/last sync\. Exactly this commit runs/)).toBeInTheDocument();
    expect(screen.queryByRole('alert', { name: /waits for the repository/ })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled();
  });

  it('holds Approve and says why while the repository needs a sync or one is running', async () => {
    renderDetail({ commit: { sha: SHA, blocked: ['repo_changed', 'sync_running'] } });
    const note = await screen.findByRole('alert', { name: 'Approval waits for the repository' });
    expect(within(note).getByText(/URL, branch or subpath changed after its last sync/)).toBeInTheDocument();
    expect(within(note).getByText(/A sync of the repository is queued or running/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
  });

  it('says when no commit is known yet', async () => {
    renderDetail({ commit: { blocked: ['never_synced'] } });
    const playbook = await screen.findByRole('region', { name: 'Playbook and repository' });
    expect(within(playbook).getByText('not known yet')).toBeInTheDocument();
    expect(screen.getByRole('alert', { name: 'Approval waits for the repository' })).toHaveTextContent('never been synced');
  });

  it('says an approved request is pinned', async () => {
    renderDetail({ commit: { sha: SHA } }, { status: 'approved', decided_at: '2026-11-16T09:05:00Z', run_group_id: RQ });
    expect(await screen.findByText(/Pinned when the request was approved/)).toBeInTheDocument();
  });

  it('shows no commit for an editor playbook', async () => {
    renderDetail({ playbook: { id: 'pb1', source: 'editor', inline_digest: 'abc' }, repo: undefined });
    await screen.findByRole('region', { name: 'Playbook and repository' });
    expect(screen.queryByText('Commit')).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    renderDetail({ commit: { sha: SHA, blocked: ['sync_running'] } }, {}, 'de');
    const note = await screen.findByRole('alert', { name: 'Die Freigabe wartet auf das Repository' });
    expect(within(note).getByText(/Ein Sync des Repositorys steht an oder läuft/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Commit-ID kopieren' })).toBeInTheDocument();
  });
});
