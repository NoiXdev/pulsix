import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { PlaybooksPage } from './PlaybooksPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const PREFS = { density: 'standard', page_size: 25, lists: {} };
const playbook = (summary: string) => ({ id: `id-${summary}`, source: 'editor', summary, created_at: '', updated_at: '2026-11-01T00:00:00Z' });

function setup() {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': ORG,
    'GET /api/v1/orgs/acme/playbooks': { playbooks: numbered('play', 30).map(playbook) },
    'GET /api/v1/orgs/acme/repos': { repos: [] },
  });
  return renderListPage({ path: '/playbooks', component: PlaybooksPage, initial: '/o/acme/playbooks' });
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

  it('has no inline buttons in rows; the row menu offers Edit for an editor playbook', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 playbooks');
    expect(screen.queryByRole('button', { name: 'play30' })).not.toBeInTheDocument();
    await openRowMenu(user, 'play30');
    await user.click(screen.getByRole('menuitem', { name: 'Edit' }));
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
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
});
