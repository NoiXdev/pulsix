import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { FleetPage } from './FleetPage.tsx';
import { validateFleetSearch } from './fleetSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const TOTALS = { crit: 0, warn: 0, unknown: 0, ok: 0, no_data: 0, all_ignored: 0 };
const host = (name: string, extra: Record<string, unknown> = {}) => ({
  host_id: `id-${name}`, name, kind: 'server', zone_id: '00000000-0000-4000-8000-000000000003', state: 'ok', crit: 0, warn: 0, unknown: 0, all_ignored: false, ...extra,
});

function setup(locale?: 'de', hosts: unknown[] = numbered('web', 30).map((n) => host(n)), initial = '/o/acme/fleet', seen: URLSearchParams[] = []) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'viewer', created_at: '' } },
    'GET /api/v1/orgs/acme/zones': { zones: [{ id: '00000000-0000-4000-8000-000000000003', name: 'Frankfurt', slug: 'fra' }] },
    'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    'GET /api/v1/orgs/acme/fleet': (req: MockRequest) => {
      seen.push(req.search);
      return { hosts, totals: TOTALS };
    },
  });
  return renderListPage({
    path: '/fleet', component: FleetPage, initial, locale, extraSearch: (q) => ({ ...validateFleetSearch(q) }),
    others: [{ path: '/hosts/$id', component: () => <div>HOST VIEW</div> }],
  });
}

describe('FleetPage list', () => {
  it('keeps the server order (worst first) until a sort is chosen', async () => {
    setup(undefined, [host('zeta', { state: 'crit', crit: 1 }), host('alpha'), host('mid', { state: 'warn', warn: 1 })]);
    await screen.findByText('1–3 of 3 hosts');
    expect(firstCells()).toEqual(['zeta', 'alpha', 'mid']);
  });

  it('sorts by host in the browser, web9 before web10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 hosts');
    await sortBy(user, 'Host');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['web1', 'web2', 'web3', 'web4', 'web5', 'web6', 'web7', 'web8', 'web9', 'web10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts the counts as numbers', async () => {
    const user = userEvent.setup();
    setup(undefined, [host('a', { crit: 10 }), host('b', { crit: 2 }), host('c', { crit: 9 })]);
    await screen.findByText('1–3 of 3 hosts');
    await sortBy(user, 'Crit');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'c', 'a']));
  });

  it('sorts the state column by severity, critical first', async () => {
    const user = userEvent.setup();
    setup(undefined, [
      host('ok-host'),
      host('nodata', { state: null }),
      host('crit-host', { state: 'crit', crit: 1 }),
      host('unknown-host', { state: 'unknown', unknown: 1 }),
      host('warn-host', { state: 'warn', warn: 1 }),
    ]);
    await screen.findByText('1–5 of 5 hosts');
    await sortBy(user, 'State');
    await waitFor(() => expect(firstCells()).toEqual(['crit-host', 'warn-host', 'unknown-host', 'ok-host', 'nodata']));
    await sortBy(user, 'State');
    await waitFor(() => expect(firstCells()).toEqual(['nodata', 'ok-host', 'unknown-host', 'warn-host', 'crit-host']));
  });

  describe('filters', () => {
    const hosts = [host('web-1', { state: 'crit', crit: 1 }), host('web-2'), host('db-1', { state: 'crit', crit: 2 })];

    it('reads the host search and state from the URL and narrows the rows', async () => {
      setup(undefined, hosts, '/o/acme/fleet?q=web&state=crit');
      await screen.findByText('1–1 of 1 host');
      expect(firstCells()).toEqual(['web-1']);
      expect(screen.getByText('1 of 3 hosts')).toBeInTheDocument();
    });

    it('sends zone, group and tag to the server and keeps them in the URL', async () => {
      const seen: URLSearchParams[] = [];
      const user = userEvent.setup();
      const router = setup(undefined, hosts, '/o/acme/fleet?zone=00000000-0000-4000-8000-000000000003&group=00000000-0000-4000-8000-000000000004', seen);
      await screen.findByText('1–3 of 3 hosts');
      expect(seen[0].get('zone')).toBe('00000000-0000-4000-8000-000000000003');
      expect(seen[0].get('group')).toBe('00000000-0000-4000-8000-000000000004');
      await user.type(screen.getByLabelText('Filter by tag'), 'role');
      await waitFor(() => expect(router.state.location.search).toEqual({ zone: '00000000-0000-4000-8000-000000000003', group: '00000000-0000-4000-8000-000000000004', tag: 'role' }));
      await waitFor(() => expect(seen.at(-1)?.get('tag')).toBe('role'));
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await waitFor(() => expect(router.state.location.search).toEqual({}));
    });
  });

  it('names a client host and its presence in the row link', async () => {
    setup(undefined, [host('laptop-1', { kind: 'client' })]);
    expect(await screen.findByRole('link', { name: 'laptop-1, Client' })).toHaveAttribute('href', '/o/acme/hosts/id-laptop-1');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 hosts');
    await hideColumn(user, 'Zone');
    expect(screen.queryByRole('columnheader', { name: 'Zone' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 hosts')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 hosts')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 hosts')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup('de');
    expect(await screen.findByText('1–25 von 30 Hosts')).toBeInTheDocument();
  });
});
