import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { PlaybooksPage } from './PlaybooksPage.tsx';
import { validatePlaybooksSearch } from './playbooksSearch.ts';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const PREFS = { density: 'standard', page_size: 25, lists: {} };
const playbook = (summary: string) => ({ id: `id-${summary}`, source: 'editor', summary, created_at: '', updated_at: '2026-11-01T00:00:00Z' });

function setup(initial = '/o/acme/playbooks', playbooks: unknown[] = numbered('play', 30).map(playbook)) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': ORG,
    'GET /api/v1/orgs/acme/playbooks': { playbooks },
    'GET /api/v1/orgs/acme/repos': { repos: [] },
  });
  return renderListPage({ path: '/playbooks', component: PlaybooksPage, initial, extraSearch: (s) => ({ ...validatePlaybooksSearch(s) }) });
}

describe('PlaybooksPage list', () => {
  it('sorts by summary in the browser, play9 before play10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 playbooks');
    await sortBy(user, 'Summary / path');
    await waitFor(() => expect(firstCells().slice(7, 10)).toEqual(['play8', 'play9', 'play10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('links every row to its playbook page', async () => {
    setup();
    await screen.findByText('1–25 of 30 playbooks');
    expect(screen.getByRole('link', { name: 'play30' })).toHaveAttribute('href', '/o/acme/playbooks/id-play30');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 playbooks');
    await hideColumn(user, 'Repository');
    expect(screen.queryByRole('columnheader', { name: 'Repository' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    await user.click(await screen.findByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 playbooks')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 playbooks')).toBeInTheDocument();
  });
  describe('filters', () => {
    const rows = [
      playbook('site'),
      { id: 'id-p1', source: 'repo', summary: '', path: 'web/deploy.yml', repo_id: 'r1', created_at: '', updated_at: '2026-11-01T00:00:00Z' },
      { id: 'id-p2', source: 'repo', summary: '', path: 'db/backup.yml', repo_id: 'r2', created_at: '', updated_at: '2026-11-01T00:00:00Z' },
    ];

    it('reads the filters from the URL and narrows the rows', async () => {
      setup('/o/acme/playbooks?source=repo&repo=r2', rows);
      expect(await screen.findByText('1–1 of 1 playbook')).toBeInTheDocument();
      expect(firstCells()).toEqual(['db/backup.yml']);
      expect(screen.getByText('1 of 3 playbooks')).toBeInTheDocument();
    });

    it('writes the source and the search to the URL', async () => {
      const user = userEvent.setup();
      const router = setup('/o/acme/playbooks', rows);
      await screen.findByText('1–3 of 3 playbooks');
      await pickOption(screen, 'Filter by source', 'editor');
      await waitFor(() => expect(router.state.location.search).toEqual({ source: 'editor' }));
      expect(firstCells()).toEqual(['site']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await user.type(screen.getByRole('searchbox', { name: 'Search playbooks' }), 'deploy');
      await waitFor(() => expect(router.state.location.search).toEqual({ q: 'deploy' }));
      expect(firstCells()).toEqual(['web/deploy.yml']);
    });

    it('says when no playbook matches', async () => {
      setup('/o/acme/playbooks?q=zzz', rows);
      expect(await screen.findByText('No playbooks match the filters.')).toBeInTheDocument();
    });
  });
});
