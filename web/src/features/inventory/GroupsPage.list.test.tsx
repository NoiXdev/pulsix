import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { GroupsPage } from './GroupsPage.tsx';
import { validateFilters } from '../lists/filterSearch.ts';
import { GROUPS_FILTERS } from './groupsSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const group = (name: string, extra: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, description: '', parent_id: null, members: 3, group_fields: 1, host_fields: 0, complete: true, revision: 1, ...extra,
});

function setup(locale?: 'de', groups: unknown[] = numbered('grp', 30).map((n) => group(n)), query = '') {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme/inventory/groups': { groups },
    'GET /api/v1/orgs/acme/bastions': { bastions: [] },
  });
  return renderListPage({
    path: '/groups', component: GroupsPage, initial: `/o/acme/groups${query}`, locale,
    extraSearch: (s) => validateFilters(GROUPS_FILTERS, s),
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
    setup(undefined, [group('a', { members: 10 }), group('b', { members: 2 }), group('c', { members: 9 })]);
    await screen.findByText('1–3 of 3 groups');
    await sortBy(user, 'Members');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'c', 'a']));
  });

  it('lists a parent before its child even when the API returns the child first, indented', async () => {
    setup(undefined, [group('kid', { parent_id: 'id-top' }), group('top')]);
    await screen.findByText('1–2 of 2 groups');
    expect(firstCells()).toEqual(['top', 'kid']);
    expect(screen.getByText('kid')).toHaveStyle({ paddingLeft: '16px' });
    expect(screen.queryByText('in top')).not.toBeInTheDocument();
  });

  it('after a sort the child has no indent and names its parent', async () => {
    const user = userEvent.setup();
    setup(undefined, [group('top'), group('zeta'), group('kid', { parent_id: 'id-top' })]);
    await screen.findByText('1–3 of 3 groups');
    await sortBy(user, 'Group');
    await waitFor(() => expect(firstCells()).toEqual(['kid in top', 'top', 'zeta']));
    expect(screen.getByText('in top')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'kid, in top' })).toBeInTheDocument();
    expect(screen.getByText(/^kid/, { selector: 'span.truncate' })).toHaveStyle({ paddingLeft: '0px' });
  });

  it('keeps the tree indent for a hand-edited sort key no column offers', async () => {
    setup(undefined, [group('kid', { parent_id: 'id-top' }), group('top')], '?sort=bogus');
    await screen.findByText('1–2 of 2 groups');
    expect(firstCells()).toEqual(['top', 'kid']);
    expect(screen.getByText('kid')).toHaveStyle({ paddingLeft: '16px' });
    expect(screen.queryByText('in top')).not.toBeInTheDocument();
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

  describe('filters', () => {
    // all_servers › web › web_eu, and db on its own.
    const rows = [
      group('db', { complete: false }),
      group('web_eu', { parent_id: 'id-web', complete: false }),
      group('all_servers'),
      group('web', { parent_id: 'id-all_servers', bastion_id: 'b1', description: 'Front tier' }),
    ];

    it('reads the filters from the URL, narrows the rows and names each one\'s path instead of indenting', async () => {
      setup(undefined, rows, '?values=incomplete');
      expect(await screen.findByText('1–2 of 2 groups')).toBeInTheDocument();
      expect(screen.getByText('2 of 4 groups')).toBeInTheDocument();
      // Tree order, flat.
      expect(firstCells()).toEqual(['web_eu in all_servers › web', 'db']);
      expect(screen.getByText(/^web_eu/, { selector: 'span.truncate' })).toHaveStyle({ paddingLeft: '0px' });
      expect(screen.getByRole('link', { name: 'web_eu, in all_servers › web' })).toBeInTheDocument();
    });

    it('keeps every group below the chosen parent', async () => {
      setup(undefined, rows, '?parent=id-all_servers');
      expect(await screen.findByText('1–2 of 2 groups')).toBeInTheDocument();
      expect(firstCells()).toEqual(['web in all_servers', 'web_eu in all_servers › web']);
    });

    it('writes the search, the parent and the chip to the URL', async () => {
      const user = userEvent.setup();
      const router = setup(undefined, rows, '?page=2');
      await screen.findByText('1–4 of 4 groups');
      // Unfiltered: the tree, indented.
      expect(firstCells()).toEqual(['all_servers', 'web', 'web_eu', 'db']);

      await user.click(screen.getByRole('button', { name: 'Has a jump host' }));
      await waitFor(() => expect(router.state.location.search).toEqual({ jump: true }));
      expect(firstCells()).toEqual(['web in all_servers']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await waitFor(() => expect(router.state.location.search).toEqual({}));

      await pickOption(screen, 'Filter by parent group', 'web');
      await waitFor(() => expect(router.state.location.search).toEqual({ parent: 'id-web' }));
      expect(firstCells()).toEqual(['web_eu in all_servers › web']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));

      await user.type(screen.getByRole('searchbox', { name: 'Search groups' }), 'front');
      await waitFor(() => expect(router.state.location.search).toEqual({ q: 'front' }));
      expect(firstCells()).toEqual(['web in all_servers']);
    });

    it('offers only groups with subgroups as parents', async () => {
      const user = userEvent.setup();
      setup(undefined, rows);
      await screen.findByText('1–4 of 4 groups');
      await user.click(screen.getByRole('combobox', { name: 'Filter by parent group' }));
      const options = screen.getAllByRole('option').map((o) => o.textContent);
      expect(options.some((o) => o?.startsWith('all_servers'))).toBe(true);
      expect(options.some((o) => o?.startsWith('web') && !o.startsWith('web_eu'))).toBe(true);
      expect(options.some((o) => o?.startsWith('db') || o?.startsWith('web_eu'))).toBe(false);
    });

    it('says when no group matches', async () => {
      setup(undefined, rows, '?q=zzz');
      expect(await screen.findByText('No groups match the filters.')).toBeInTheDocument();
    });
  });
});
