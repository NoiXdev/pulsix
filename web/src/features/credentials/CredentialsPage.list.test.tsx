import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { CredentialsPage } from './CredentialsPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const cred = (name: string) => ({
  id: `id-${name}`, name, kind: 'ssh_key', username: 'ansible', key_type: 'ed25519', expired: false, expires_soon: false,
  used_by: { bastions: 0, hosts: 1 }, created_at: '2026-06-01T00:00:00Z', updated_at: '2026-06-01T00:00:00Z',
});

function setup(locale?: 'de', role = 'admin') {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } },
    'GET /api/v1/orgs/acme/credentials': { credentials: numbered('cred', 30).map(cred) },
    'GET /api/v1/orgs/acme/credentials/id-cred30': { credential: cred('cred30') },
  });
  return renderListPage({ path: '/credentials', component: CredentialsPage, initial: '/o/acme/credentials', locale });
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
});
