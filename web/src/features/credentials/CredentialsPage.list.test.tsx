import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { CredentialsPage } from './CredentialsPage.tsx';
import { validateCredentialsSearch } from './credentialsSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const cred = (name: string, extra: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, kind: 'ssh_key', username: 'ansible', key_type: 'ed25519', expired: false, expires_soon: false,
  used_by: { bastions: 0, hosts: 1 }, created_at: '2026-06-01T00:00:00Z', updated_at: '2026-06-01T00:00:00Z', ...extra,
});

function setup(locale?: 'de', role = 'admin', initial = '/o/acme/credentials', credentials: unknown[] = numbered('cred', 30).map((n) => cred(n))) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } },
    'GET /api/v1/orgs/acme/credentials': { credentials },
    'GET /api/v1/orgs/acme/credentials/id-cred30': { credential: cred('cred30') },
  });
  return renderListPage({ path: '/credentials', component: CredentialsPage, initial, locale, extraSearch: (s) => ({ ...validateCredentialsSearch(s) }) });
}

describe('CredentialsPage list', () => {
  it('sorts by name in the browser, cred9 before cred10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 credentials');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['cred1', 'cred2', 'cred3', 'cred4', 'cred5', 'cred6', 'cred7', 'cred8', 'cred9', 'cred10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('opens a credential from its name and from the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 credentials');
    await openRowMenu(user, 'cred30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open']);
    await user.click(screen.getByRole('menuitem', { name: 'Open' }));
    expect(await screen.findByRole('dialog', { name: 'cred30' })).toBeInTheDocument();
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 credentials');
    await hideColumn(user, 'Kind');
    expect(screen.queryByRole('columnheader', { name: 'Kind' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 credentials')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 credentials')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 credentials')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup('de');
    expect(await screen.findByText('1–25 von 30 Zugangsdaten')).toBeInTheDocument();
  });

  describe('filters', () => {
    const rows = [
      cred('deploy'),
      cred('old', { expired: true, expires_soon: true }),
      cred('soon', { expires_soon: true, kind: 'git_token' }),
      cred('idle', { used_by: { bastions: 0, hosts: 0 } }),
    ];

    it('reads the filters from the URL and narrows the rows', async () => {
      setup(undefined, 'admin', '/o/acme/credentials?kind=git_token', rows);
      expect(await screen.findByText('1–1 of 1 credential')).toBeInTheDocument();
      expect(firstCells()).toEqual(['soon']);
      expect(screen.getByText('1 of 4 credentials')).toBeInTheDocument();
    });

    it('writes the search and the chips to the URL', async () => {
      const user = userEvent.setup();
      const router = setup(undefined, 'admin', '/o/acme/credentials?page=2', rows);
      await screen.findByText('1–4 of 4 credentials');
      await user.click(screen.getByRole('button', { name: 'Expired' }));
      await waitFor(() => expect(router.state.location.search).toEqual({ expired: true }));
      expect(firstCells()).toEqual(['old']);
      await user.click(screen.getByRole('button', { name: 'Expired' }));
      await user.click(screen.getByRole('button', { name: 'Unused' }));
      await waitFor(() => expect(firstCells()).toEqual(['idle']));
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await waitFor(() => expect(router.state.location.search).toEqual({}));
      expect(firstCells()).toHaveLength(4);
      await user.type(screen.getByRole('searchbox', { name: 'Search credentials' }), 'dep');
      await waitFor(() => expect(router.state.location.search).toEqual({ q: 'dep' }));
      expect(firstCells()).toEqual(['deploy']);
    });

    it('says when no credential matches', async () => {
      setup(undefined, 'admin', '/o/acme/credentials?q=zzz', rows);
      expect(await screen.findByText('No credentials match the filters.')).toBeInTheDocument();
    });
  });
});
