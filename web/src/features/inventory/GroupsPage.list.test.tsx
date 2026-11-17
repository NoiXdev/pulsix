import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { GroupsPage } from './GroupsPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const group = (name: string) => ({
  id: `id-${name}`, name, description: '', parent_id: null, members: 3, group_fields: 1, host_fields: 0, complete: true, revision: 1,
});

function setup(locale?: 'de') {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme/inventory/groups': { groups: numbered('grp', 30).map(group) },
    'GET /api/v1/orgs/acme/bastions': { bastions: [] },
  });
  return renderListPage({
    path: '/groups', component: GroupsPage, initial: '/o/acme/groups', locale,
    others: [{ path: '/groups/$id', component: () => <div>GROUP DETAIL</div> }],
  });
}

describe('GroupsPage list', () => {
  it('sorts by name in the browser, grp9 before grp10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 groups');
    await sortBy(user, 'Group');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['grp1', 'grp2', 'grp3', 'grp4', 'grp5', 'grp6', 'grp7', 'grp8', 'grp9', 'grp10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts by members as a number', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 groups');
    await sortBy(user, 'Members');
    expect(screen.getByRole('columnheader', { name: /Members/ })).toHaveAttribute('aria-sort', 'ascending');
  });

  it('has no row menu and no inline buttons; each row is a link', async () => {
    setup();
    await screen.findByText('1–25 of 30 groups');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
    expect(screen.getByRole('link', { name: 'grp30' })).toHaveAttribute('href', '/o/acme/groups/id-grp30');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 groups');
    await hideColumn(user, 'Jump host');
    expect(screen.queryByRole('columnheader', { name: 'Jump host' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 groups')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 groups')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 groups')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup('de');
    expect(await screen.findByText('1–25 von 30 Gruppen')).toBeInTheDocument();
  });
});
