import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { ItemsPage } from './ItemsPage.tsx';
import { validateDecisionsSearch } from './decisionsSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const decision = (pattern: string, over: Record<string, unknown> = {}) => ({
  id: `id-${pattern}`, scope: 'org', check_key: 'disk', match_kind: 'exact', pattern, decision: 'ignore', note: '', created_at: '2026-09-01T00:00:00Z',
  updated_at: '2026-09-01T00:00:00Z', ...over,
});

function setup(opts: { locale?: 'de'; role?: string; decisions?: unknown[]; onDelete?: () => void; initial?: string } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': org(opts.role ?? 'operator'),
    'GET /api/v1/orgs/acme/rules/metrics': { metrics: [{ name: 'disk.used_pct', check_key: 'disk', kind: 'numeric' }] },
    'GET /api/v1/orgs/acme/item-decisions': { decisions: opts.decisions ?? numbered('p', 30).map((n) => decision(n)) },
    'DELETE /api/v1/orgs/acme/item-decisions/id-p30': () => {
      opts.onDelete?.();
      return {};
    },
  });
  return renderListPage({
    path: '/checks/items', component: ItemsPage, initial: opts.initial ?? '/o/acme/checks/items', locale: opts.locale, extraSearch: (s) => ({ ...validateDecisionsSearch(s) }),
    others: [
      { path: '/checks', component: () => <div>CHECKS</div> },
      { path: '/checks/monitoring-groups', component: () => <div>GROUPS</div> },
      { path: '/checks/rules', component: () => <div>RULES</div> },
      { path: '/hosts/$id', component: () => <div>HOST</div> },
    ],
  });
}

const patterns = () => firstCells().map((c) => c.replace(/\(exact\)$/, '').trim());

describe('ItemsPage list', () => {
  it('sorts by pattern in the browser, p9 before p10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 decisions');
    await sortBy(user, 'Pattern');
    await waitFor(() => expect(patterns().slice(0, 10)).toEqual(['p1', 'p2', 'p3', 'p4', 'p5', 'p6', 'p7', 'p8', 'p9', 'p10']));
    expect(router.state.location.search).toMatchObject({ sort: 'pattern', order: 'asc' });
  });

  it('keeps a check own decisions together with the organization one first, until a sort is chosen', async () => {
    setup({
      decisions: [
        decision('h', { scope: 'host', host_id: 'h1' }),
        decision('svc', { check_key: 'service' }),
        decision('o'),
      ],
    });
    await screen.findByText('1–3 of 3 decisions');
    expect(patterns()).toEqual(['o', 'h', 'svc']);
  });

  it('moves Edit and Delete into the row menu with the same confirmation', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 decisions');
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    await chooseRowAction(user, 'p30 (Organization)', 'Delete…');
    expect(await acceptConfirm(user)).toContain('pattern "p30"');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('gives a viewer no row menu', async () => {
    setup({ role: 'viewer' });
    await screen.findByText('1–25 of 30 decisions');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 decisions');
    await hideColumn(user, 'Note');
    expect(screen.queryByRole('columnheader', { name: 'Note' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 decisions')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 decisions')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 decisions')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Entscheidungen')).toBeInTheDocument();
  });

  describe('filters', () => {
    const decisions = [decision('a'), decision('b', { decision: 'watch' }), decision('c', { check_key: 'service' })];

    it('reads the verdict and check from the URL and narrows the rows', async () => {
      setup({ decisions, initial: '/o/acme/checks/items?check=disk&verdict=watch' });
      await screen.findByText('1–1 of 1 decision');
      expect(patterns()).toEqual(['b']);
      expect(screen.getByText('1 of 3 decisions')).toBeInTheDocument();
    });

    it('writes the check to the URL and resets', async () => {
      const user = userEvent.setup();
      const router = setup({ decisions, initial: '/o/acme/checks/items?page=2' });
      await screen.findByText('1–3 of 3 decisions');
      await pickOption(screen, 'Filter by check', 'service');
      await waitFor(() => expect(router.state.location.search).toEqual({ check: 'service' }));
      expect(patterns()).toEqual(['c']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await waitFor(() => expect(router.state.location.search).toEqual({}));
    });
  });
});
