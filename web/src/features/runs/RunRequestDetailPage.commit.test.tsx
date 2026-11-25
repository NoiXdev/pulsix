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
    expect(within(playbook).getByText(/last sync\. The requested run runs exactly this commit/)).toBeInTheDocument();
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

  it('says an approved request ran its pinned commit, without the pending texts', async () => {
    renderDetail(
      { commit: { sha: SHA, blocked: ['sync_running'] } },
      { status: 'approved', decided_at: '2026-11-16T09:05:00Z', run_group_id: RQ },
    );
    expect(await screen.findByText(/The approved run ran this commit/)).toBeInTheDocument();
    expect(screen.queryByText(/exactly this commit/)).not.toBeInTheDocument();
    expect(screen.queryByRole('alert', { name: 'Approval waits for the repository' })).not.toBeInTheDocument();
  });

  it.each(['rejected', 'expired'])('shows a %s request its commit, but neither the waiting alert nor what runs', async (status) => {
    renderDetail({ commit: { sha: SHA, blocked: ['repo_changed'] } }, { status, decided_at: '2026-11-16T09:05:00Z' });
    expect(await screen.findByText(SHA.slice(0, 12))).toBeInTheDocument();
    expect(screen.queryByRole('alert', { name: 'Approval waits for the repository' })).not.toBeInTheDocument();
    expect(screen.queryByText(/exactly this commit|ran this commit/)).not.toBeInTheDocument();
  });

  it("labels a retry's commit as the original run's", async () => {
    renderDetail({ commit: { sha: SHA, source: 'original_run' } }, { kind: 'retry_failed' });
    expect(await screen.findByText(/The commit the original run used/)).toBeInTheDocument();
    expect(screen.queryByText(/last sync/)).not.toBeInTheDocument();
  });

  it('says what each follow-up runs', async () => {
    renderDetail({
      commit: { sha: SHA },
      follow_ups: {
        start: true,
        templates: [
          { template_id: 'f1', template_name: 'db', template_revision: 2, commit: 'pinned',
            follow_ups: [{ template_id: 'f3', template_name: 'apps-deploy', template_revision: 1, commit: 'current', repo: 'apps' }] },
          { template_id: 'f2', template_name: 'notify', template_revision: 1, commit: 'editor' },
        ],
      },
    });
    const section = await screen.findByRole('region', { name: 'Follow-ups' });
    expect(within(section).getByText(/runs the same commit/)).toBeInTheDocument();
    expect(within(section).getByText(/runs the current state of apps/)).toBeInTheDocument();
    expect(within(section).getByText(/runs its playbook as saved when it starts/)).toBeInTheDocument();
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
    expect(screen.getByText(/Der angefragte Lauf führt genau diesen Commit aus/)).toBeInTheDocument();
  });
});
