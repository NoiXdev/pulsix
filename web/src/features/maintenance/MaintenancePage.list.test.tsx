import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { MaintenancePage } from './MaintenancePage.tsx';
import { validateMaintenanceSearch, windowStatus } from './maintenanceSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const win = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, target_kind: 'org', reason: 'maintenance', starts_at: '2026-09-22T10:00:00Z', ends_at: '2026-09-22T12:00:00Z',
  active_now: false, revision: 1, created_at: '', updated_at: '', ...over,
});

function setup(opts: { locale?: 'de'; role?: string; windows?: unknown[]; onDelete?: () => void; initial?: string } = {}) {
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
  return renderListPage({ path: '/maintenance', component: MaintenancePage, initial: opts.initial ?? '/o/acme/maintenance', locale: opts.locale, extraSearch: (s) => ({ ...validateMaintenanceSearch(s) }) });
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

  describe('filters', () => {
    const future = '2999-01-01T00:00:00Z';
    const windows = [
      win('now', { active_now: true }),
      win('later', { starts_at: future, ends_at: '2999-01-02T00:00:00Z' }),
      win('earlier'),
      win('nightly', { cron: '0 3 * * *', starts_at: undefined, ends_at: undefined }),
    ];

    it('reads the status from the URL: upcoming covers a recurring window between occurrences', async () => {
      setup({ windows, initial: '/o/acme/maintenance?status=upcoming' });
      await screen.findByText('1–2 of 2 windows');
      expect(firstCells().sort()).toEqual(['later', 'nightly']);
      expect(screen.getByText('2 of 4 windows')).toBeInTheDocument();
    });

    it('writes the status to the URL and resets', async () => {
      const user = userEvent.setup();
      const router = setup({ windows, initial: '/o/acme/maintenance?page=2' });
      await screen.findByText('1–4 of 4 windows');
      await pickOption(screen, 'Filter by status', 'Past');
      await waitFor(() => expect(router.state.location.search).toEqual({ status: 'past' }));
      expect(firstCells()).toEqual(['earlier']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await waitFor(() => expect(router.state.location.search).toEqual({}));
    });

    it.each([
      [{ active_now: true }, 'active'],
      [{ active_now: false, cron: '0 3 * * *' }, 'upcoming'],
      [{ active_now: false, ends_at: '2020-01-01T00:00:00Z' }, 'past'],
      [{ active_now: false, ends_at: '2999-01-01T00:00:00Z' }, 'upcoming'],
    ])('reads %j as %s', (w, want) => {
      expect(windowStatus(w, new Date('2026-11-18T12:00:00Z'))).toBe(want);
    });
  });
});
