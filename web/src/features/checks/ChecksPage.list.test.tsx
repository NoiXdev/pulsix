import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { ChecksPage } from './ChecksPage.tsx';
import { check, GROUPS, org } from './checksPageTestKit.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };

function setup(opts: { locale?: 'de'; role?: string; checks?: unknown[]; onDelete?: () => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': org(opts.role ?? 'operator'),
    'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
    'GET /api/v1/orgs/acme/checks': { checks: opts.checks ?? numbered('chk', 30).map((k) => check({ id: `id-${k}`, key: k, origin: 'custom' })) },
    'DELETE /api/v1/orgs/acme/checks/id-chk30': () => {
      opts.onDelete?.();
      return {};
    },
  });
  return renderListPage({
    path: '/checks', component: ChecksPage, initial: '/o/acme/checks', locale: opts.locale,
    others: [
      { path: '/checks/monitoring-groups', component: () => <div>GROUPS</div> },
      { path: '/checks/rules', component: () => <div>RULES</div> },
      { path: '/checks/items', component: () => <div>ITEMS</div> },
      { path: '/hosts/$id', component: () => <div>HOST</div> },
    ],
  });
}

describe('ChecksPage list', () => {
  it('sorts by check in the browser, chk9 before chk10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 checks');
    await sortBy(user, 'Check');
    await waitFor(() => expect(firstCells().slice(0, 10).map((c) => c.replace(/custom$/, ''))).toEqual(['chk1', 'chk2', 'chk3', 'chk4', 'chk5', 'chk6', 'chk7', 'chk8', 'chk9', 'chk10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts the interval as a number', async () => {
    const user = userEvent.setup();
    setup({ checks: [check({ id: 'a', key: 'a', interval_seconds: 600 }), check({ id: 'b', key: 'b', interval_seconds: 60 }), check({ id: 'c', key: 'c', interval_seconds: 300 })] });
    await screen.findByText('1–3 of 3 checks');
    await sortBy(user, 'Interval');
    await waitFor(() => expect(firstCells().map((c) => c.replace(/seed$/, ''))).toEqual(['b', 'c', 'a']));
  });

  it('keeps Delete in the row menu and asks the same confirmation before deleting', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 checks');
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    await chooseRowAction(user, 'chk30 (Organization)', 'Delete…');
    expect(await acceptConfirm(user)).toContain('Delete check chk30?');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('opens the editor from the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 checks');
    await chooseRowAction(user, 'chk30 (Organization)', 'Edit');
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  it('gives a viewer no row menu', async () => {
    setup({ role: 'viewer' });
    await screen.findByText('1–25 of 30 checks');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
  });

  it('offers Copy only for a seeded or shared definition, and no Delete for a shared one', async () => {
    const user = userEvent.setup();
    setup({ checks: [check({ id: 's', key: 'seeded' }), check({ id: 'x', key: 'shared', origin: 'shared' }), check({ id: 'u', key: 'custom1', origin: 'custom' })] });
    await screen.findByText('1–3 of 3 checks');
    const items = async (name: string) => {
      await openRowMenu(user, `${name} (Organization)`);
      const found = screen.getAllByRole('menuitem').map((el) => el.textContent);
      await user.keyboard('{Escape}');
      return found;
    };
    expect(await items('seeded')).toEqual(['Edit', 'Copy', 'Delete…']);
    expect(await items('shared')).toEqual(['Edit', 'Copy']);
    expect(await items('custom1')).toEqual(['Edit', 'Delete…']);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 checks');
    await hideColumn(user, 'Plugin');
    expect(screen.queryByRole('columnheader', { name: 'Plugin' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 checks')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 checks')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 checks')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Checks')).toBeInTheDocument();
  });
});
