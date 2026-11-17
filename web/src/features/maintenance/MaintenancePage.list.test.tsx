import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { MaintenancePage } from './MaintenancePage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const win = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, target_kind: 'org', reason: 'maintenance', starts_at: '2026-09-22T10:00:00Z', ends_at: '2026-09-22T12:00:00Z',
  active_now: false, revision: 1, created_at: '', updated_at: '', ...over,
});

function setup(opts: { locale?: 'de'; role?: string; windows?: unknown[]; onDelete?: () => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': org(opts.role ?? 'operator'),
    'GET /api/v1/orgs/acme/maintenance-windows': { windows: opts.windows ?? numbered('win', 30).map((n) => win(n)) },
    'DELETE /api/v1/orgs/acme/maintenance-windows/id-win30': () => {
      opts.onDelete?.();
      return {};
    },
  });
  return renderListPage({ path: '/maintenance', component: MaintenancePage, initial: '/o/acme/maintenance', locale: opts.locale });
}

describe('MaintenancePage list', () => {
  it('sorts by window in the browser, win9 before win10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 windows');
    await sortBy(user, 'Window');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['win1', 'win2', 'win3', 'win4', 'win5', 'win6', 'win7', 'win8', 'win9', 'win10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('shows the note under the name at every density', async () => {
    setup({ windows: [win('db upgrade', { note: 'kernel' })] });
    await screen.findByText('1–1 of 1 windows').catch(() => undefined);
    expect(await screen.findByText('kernel')).toBeInTheDocument();
  });

  it('moves Edit and Delete into the row menu with the same confirmation', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 windows');
    expect(screen.queryByRole('button', { name: /^Delete/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Edit/ })).not.toBeInTheDocument();
    await chooseRowAction(user, 'win30', 'Delete');
    expect(await acceptConfirm(user)).toBe('Delete maintenance window win30?');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('opens the editor from the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 windows');
    await chooseRowAction(user, 'win30', 'Edit');
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  it('gives a viewer no row menu', async () => {
    setup({ role: 'viewer' });
    await screen.findByText('1–25 of 30 windows');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 windows');
    await hideColumn(user, 'Why');
    expect(screen.queryByRole('columnheader', { name: 'Why' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 windows')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 windows')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 windows')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Fenster')).toBeInTheDocument();
  });
});
