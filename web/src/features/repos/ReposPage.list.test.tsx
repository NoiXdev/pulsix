import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { findConfirmDialog } from '../../test/confirmDialog.ts';
import { firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { ReposPage } from './ReposPage.tsx';
import { validateReposSearch } from './reposSearch.ts';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const PREFS = { density: 'standard', page_size: 25, lists: {} };
const repo = (name: string, extra: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, url: `https://git.example/acme/${name}.git`, branch: 'main', subpath: '', created_at: '', updated_at: '', ...extra,
});

function setup(initial = '/o/acme/repos', locale?: 'de', role = 'operator', repos: unknown[] = numbered('repo', 30).map((n) => repo(n))) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { ...ORG.org, role } },
    'GET /api/v1/orgs/acme/repos': { repos },
    'GET /api/v1/orgs/acme/credentials': { credentials: [] },
  });
  return renderListPage({ path: '/repos', component: ReposPage, initial, locale, extraSearch: (s) => ({ ...validateReposSearch(s) }), others: [{ path: '/runs/$id', component: () => <div>RUN VIEW</div> }] });
}

describe('ReposPage list', () => {
  it('sorts by name in the browser, repo9 before repo10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 repositories');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['repo1', 'repo2', 'repo3', 'repo4', 'repo5', 'repo6', 'repo7', 'repo8', 'repo9', 'repo10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('keeps no inline action buttons; the row menu opens the same delete confirmation', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 repositories');
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Sync repo30' })).not.toBeInTheDocument();
    await openRowMenu(user, 'repo30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Sync', 'Triggers', 'Edit', 'Delete']);
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    const dialog = await findConfirmDialog();
    expect(dialog).toHaveTextContent('Delete repository repo30?');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 repositories');
    expect(screen.getByRole('columnheader', { name: 'URL' })).toBeInTheDocument();
    await hideColumn(user, 'URL');
    expect(screen.queryByRole('columnheader', { name: 'URL' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 repositories')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 repositories')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 repositories')).toBeInTheDocument();
  });

  it('writes the footer, the column and the menu in German', async () => {
    const user = userEvent.setup();
    setup('/o/acme/repos', 'de');
    expect(await screen.findByText('1–25 von 30 Repositories')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Zugangsdaten' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Aktionen für repo30' }));
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Synchronisieren', 'Trigger', 'Bearbeiten', 'Löschen']);
  });

  it('offers a viewer only Triggers', async () => {
    const user = userEvent.setup();
    setup('/o/acme/repos', undefined, 'viewer');
    await screen.findByText('1–25 of 30 repositories');
    await openRowMenu(user, 'repo30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Triggers']);
  });
  describe('filters', () => {
    const rows = [
      repo('fresh', { last_sync: '2026-06-01T00:00:00Z', credential_id: 'c1' }),
      repo('broken', { last_sync_error: 'boom', credential_id: 'c2' }),
      repo('idle'),
    ];

    it('reads the filters from the URL and narrows the rows', async () => {
      setup('/o/acme/repos?sync=failed', undefined, 'operator', rows);
      expect(await screen.findByText('1–1 of 1 repository')).toBeInTheDocument();
      expect(firstCells()).toEqual(['broken']);
      expect(screen.getByText('1 of 3 repositories')).toBeInTheDocument();
    });

    it('filters by credential and writes the search to the URL', async () => {
      const user = userEvent.setup();
      const router = setup('/o/acme/repos?credential=c1', undefined, 'operator', rows);
      await screen.findByText('1–1 of 1 repository');
      expect(firstCells()).toEqual(['fresh']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await waitFor(() => expect(router.state.location.search).toEqual({}));
      await user.type(screen.getByRole('searchbox', { name: 'Search repositories' }), 'idl');
      await waitFor(() => expect(router.state.location.search).toEqual({ q: 'idl' }));
      expect(firstCells()).toEqual(['idle']);
    });

    it('says when no repository matches', async () => {
      setup('/o/acme/repos?q=zzz', undefined, 'operator', rows);
      expect(await screen.findByText('No repositories match the filters.')).toBeInTheDocument();
    });
  });
});
