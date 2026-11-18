import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { MonitoringGroupsPage } from './MonitoringGroupsPage.tsx';
import { validateMonitoringGroupsSearch } from './monitoringGroupsSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const group = (name: string, priority = 0) => ({ id: `id-${name}`, name, selector: 'role=web', priority, created_at: '', updated_at: '' });

function setup(opts: { locale?: 'de'; role?: string; groups?: unknown[]; onDelete?: () => void; initial?: string } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': org(opts.role ?? 'operator'),
    'GET /api/v1/orgs/acme/monitoring-groups': { groups: opts.groups ?? numbered('mg', 30).map((n) => group(n)) },
    'DELETE /api/v1/orgs/acme/monitoring-groups/id-mg30': () => {
      opts.onDelete?.();
      return {};
    },
  });
  return renderListPage({
    path: '/checks/monitoring-groups', component: MonitoringGroupsPage, initial: opts.initial ?? '/o/acme/checks/monitoring-groups', locale: opts.locale, extraSearch: (s) => ({ ...validateMonitoringGroupsSearch(s) }),
    others: [{ path: '/checks', component: () => <div>CHECKS</div> }, { path: '/checks/rules', component: () => <div>RULES</div> }, { path: '/checks/items', component: () => <div>ITEMS</div> }],
  });
}

describe('MonitoringGroupsPage list', () => {
  it('sorts by group in the browser, mg9 before mg10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 monitoring groups');
    await sortBy(user, 'Group');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['mg1', 'mg2', 'mg3', 'mg4', 'mg5', 'mg6', 'mg7', 'mg8', 'mg9', 'mg10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts the priority as a number', async () => {
    const user = userEvent.setup();
    setup({ groups: [group('a', 10), group('b', 2), group('c', 9)] });
    await screen.findByText('1–3 of 3 monitoring groups');
    await sortBy(user, 'Priority');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'c', 'a']));
  });

  it('has Edit and Delete in the row menu for a writer, the same confirmation, no inline buttons', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 monitoring groups');
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    await chooseRowAction(user, 'mg30', 'Delete…');
    expect(await acceptConfirm(user)).toContain('Delete monitoring group mg30?');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('gives a viewer no row menu', async () => {
    setup({ role: 'viewer' });
    await screen.findByText('1–25 of 30 monitoring groups');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 monitoring groups');
    await hideColumn(user, 'Selector');
    expect(screen.queryByRole('columnheader', { name: 'Selector' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 monitoring groups')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 monitoring groups')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 monitoring groups')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Überwachungsgruppen')).toBeInTheDocument();
  });

  describe('filters', () => {
    const groups = [group('web-tier'), { ...group('db-tier'), selector: 'role=db' }];

    it('reads the search from the URL, over the name and the selector', async () => {
      setup({ groups, initial: '/o/acme/checks/monitoring-groups?q=role%3Ddb' });
      await screen.findByText('1–1 of 1 monitoring group');
      expect(firstCells()).toEqual(['db-tier']);
      expect(screen.getByText('1 of 2 monitoring groups')).toBeInTheDocument();
    });

    it('writes the search to the URL and says when nothing matches', async () => {
      const user = userEvent.setup();
      const router = setup({ groups, initial: '/o/acme/checks/monitoring-groups?page=2' });
      await screen.findByText('1–2 of 2 monitoring groups');
      await user.type(screen.getByRole('searchbox', { name: 'Search monitoring groups' }), 'zzz');
      await waitFor(() => expect(router.state.location.search).toEqual({ q: 'zzz' }));
      expect(await screen.findByText('No monitoring groups match the filter.')).toBeInTheDocument();
    });
  });
});
