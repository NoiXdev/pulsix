import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { RulesPage } from './RulesPage.tsx';
import { validateRulesSearch } from './rulesSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const rule = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, enabled: true, origin: 'user', scope: 'org', check_key: 'cpu', metric: 'cpu.used_pct', severity: 'warn', mode: 'form',
  form: { metric: 'cpu.used_pct', op: '>=', value: 90 }, source: 'cpu.used_pct >= 90', for_seconds: 0, created_at: '', updated_at: '', ...over,
});

function setup(opts: { locale?: 'de'; role?: string; rules?: unknown[]; onDelete?: () => void; initial?: string } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': org(opts.role ?? 'operator'),
    'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    'GET /api/v1/orgs/acme/rules/metrics': { metrics: [{ name: 'cpu.used_pct', check_key: 'cpu', kind: 'numeric' }] },
    'GET /api/v1/orgs/acme/rules': { rules: opts.rules ?? numbered('rule', 30).map((n) => rule(n)) },
    'DELETE /api/v1/orgs/acme/rules/id-rule30': () => {
      opts.onDelete?.();
      return {};
    },
  });
  return renderListPage({
    path: '/checks/rules', component: RulesPage, initial: opts.initial ?? '/o/acme/checks/rules', locale: opts.locale, extraSearch: (s) => ({ ...validateRulesSearch(s) }),
    others: [
      { path: '/checks', component: () => <div>CHECKS</div> },
      { path: '/checks/monitoring-groups', component: () => <div>GROUPS</div> },
      { path: '/checks/items', component: () => <div>ITEMS</div> },
      { path: '/hosts/$id', component: () => <div>HOST</div> },
    ],
  });
}

const names = () => firstCells().map((c) => c.replace(/(user|seed)$/, ''));

describe('RulesPage list', () => {
  it('sorts by rule in the browser, rule9 before rule10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 rules');
    await sortBy(user, 'Rule');
    await waitFor(() => expect(names().slice(0, 10)).toEqual(['rule1', 'rule2', 'rule3', 'rule4', 'rule5', 'rule6', 'rule7', 'rule8', 'rule9', 'rule10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('keeps rules on one metric together, and names the metric in a column, until a sort is chosen', async () => {
    setup({ rules: [rule('a', { metric: 'cpu.used_pct' }), rule('b', { metric: 'mem.used_pct' }), rule('c', { metric: 'cpu.used_pct' })] });
    await screen.findByText('1–3 of 3 rules');
    expect(names()).toEqual(['a', 'c', 'b']);
    expect(screen.getByRole('columnheader', { name: 'Metric' })).toBeInTheDocument();
    expect(screen.getAllByText('cpu.used_pct')).toHaveLength(2);
  });

  it('moves Edit and Delete into the row menu with the same confirmation', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 rules');
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    await chooseRowAction(user, 'rule30', 'Delete…');
    expect(await acceptConfirm(user)).toContain('Delete rule rule30?');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('gives a viewer no row menu', async () => {
    setup({ role: 'viewer' });
    await screen.findByText('1–25 of 30 rules');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 rules');
    await hideColumn(user, 'Severity');
    expect(screen.queryByRole('columnheader', { name: 'Severity' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 rules')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 rules')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 rules')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Regeln')).toBeInTheDocument();
  });

  describe('filters', () => {
    const rules = [rule('cpu-hot', { severity: 'crit' }), rule('cpu-warm'), rule('disk-full', { severity: 'crit', enabled: false, metric: 'disk.used_pct' })];

    it('reads severity and state from the URL and narrows the rows', async () => {
      setup({ rules, initial: '/o/acme/checks/rules?severity=crit&state=enabled' });
      await screen.findByText('1–1 of 1 rule');
      expect(names()).toEqual(['cpu-hot']);
      expect(screen.getByText('1 of 3 rules')).toBeInTheDocument();
    });

    it('writes the severity and the search to the URL', async () => {
      const user = userEvent.setup();
      const router = setup({ rules, initial: '/o/acme/checks/rules?page=2' });
      await screen.findByText('1–3 of 3 rules');
      await pickOption(screen, 'Filter by severity', 'crit');
      await waitFor(() => expect(router.state.location.search).toEqual({ severity: 'crit' }));
      expect(names().sort()).toEqual(['cpu-hot', 'disk-full']);
      await user.type(screen.getByRole('searchbox', { name: 'Search rules' }), 'disk');
      await waitFor(() => expect(router.state.location.search).toEqual({ severity: 'crit', q: 'disk' }));
      expect(names()).toEqual(['disk-full']);
    });
  });
});
