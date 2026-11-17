import { useState } from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import { renderHook, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { DataList, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Preferences } from '../../api/types.ts';
import { useListView } from './useListView.ts';

type Row = { id: string; name: string; zone: string };

const COLUMNS: DataListColumn<Row>[] = [
  { id: 'name', label: 'Host', width: '1fr', required: true, defaultVisible: true, sortKey: 'name', sortValue: (r) => r.name, render: (r) => r.name },
  { id: 'zone', label: 'Zone', width: '1fr', defaultVisible: true, sortKey: 'zone', sortValue: (r) => r.zone, render: (r) => r.zone },
  { id: 'tags', label: 'Tags', width: '1fr', defaultVisible: true, render: () => 'tag' },
  { id: 'os', label: 'Operating system', width: '1fr', defaultVisible: false, render: () => 'linux' },
];

const HOSTS: Row[] = Array.from({ length: 30 }, (_, i) => ({ id: `h${i + 1}`, name: `host${i + 1}`, zone: i % 2 ? 'east' : 'west' }));

const count = (n: number) => `${n} hosts`;

function ServerList({ total, urlState, withFilter }: { total: number | undefined; urlState?: boolean; withFilter?: boolean }) {
  const [filter, setFilter] = useState('');
  const view = useListView('hosts', COLUMNS, { mode: 'server', urlState, resetKey: withFilter ? filter : undefined });
  return (
    <>
      <output data-testid="query">{JSON.stringify(view.serverQuery)}</output>
      <output data-testid="density">{view.density}</output>
      {withFilter && (
        <button type="button" onClick={() => setFilter('web')}>
          Filter
        </button>
      )}
      <DataList label="Hosts" columns={COLUMNS} rows={HOSTS.slice(0, 2)} rowKey={(r) => r.id} empty="No hosts" {...view.listProps(total, count)} />
    </>
  );
}

function ClientList({ rows }: { rows: Row[] | undefined }) {
  const view = useListView('hosts', COLUMNS, { mode: 'client', rowKey: (r) => r.id });
  const page = view.view(rows);
  return (
    <>
      <output data-testid="rows">{page.rows.map((r) => r.name).join(',')}</output>
      <DataList label="Hosts" columns={COLUMNS} rows={page.rows} rowKey={(r) => r.id} empty="No hosts" {...view.listProps(page.total, count)} />
    </>
  );
}

let puts: Preferences[] = [];

function api(prefs: Partial<Preferences> = {}, put?: (req: MockRequest) => unknown) {
  puts = [];
  mockApi({
    'GET /api/v1/auth/me/preferences': { density: 'standard', page_size: 25, lists: {}, ...prefs },
    'PUT /api/v1/auth/me/preferences':
      put ??
      ((req: MockRequest) => {
        puts.push(req.body as Preferences);
        return req.body;
      }),
  });
}

function server(initial: string, props: Parameters<typeof ServerList>[0]) {
  return renderRoutes([{ path: '/hosts', component: () => <ServerList {...props} /> }, { path: '/elsewhere', component: () => <p>ELSEWHERE</p> }], initial);
}

const query = () => JSON.parse(screen.getByTestId('query').textContent ?? '{}') as Record<string, unknown>;
const headers = () => screen.getAllByRole('columnheader').map((h) => h.textContent?.trim()).filter(Boolean);
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function openColumns(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', { name: /^Columns/ }));
  return screen.getByRole('dialog', { name: 'Columns' });
}

afterEach(() => {
  puts = [];
});

describe('useListView in server mode', () => {
  it('reads sort, order and page from the URL', async () => {
    api();
    server('/o/acme/hosts?sort=name&order=desc&page=2', { total: 60 });

    await waitFor(() => expect(query()).toEqual({ page: 2, per_page: 25, sort: 'name', order: 'desc' }));
    expect(screen.getByRole('columnheader', { name: /Host/ })).toHaveAttribute('aria-sort', 'descending');
    expect(screen.getByRole('button', { name: 'Page 2' })).toHaveAttribute('aria-current', 'page');
  });

  it('asks for page 1 without a sort by default', async () => {
    api();
    server('/o/acme/hosts', { total: 60 });

    await waitFor(() => expect(query()).toEqual({ page: 1, per_page: 25 }));
  });

  it('ignores a sort key no column offers', async () => {
    api();
    server('/o/acme/hosts?sort=tags&order=desc', { total: 60 });

    await waitFor(() => expect(query()).toEqual({ page: 1, per_page: 25 }));
    expect(screen.getByRole('columnheader', { name: /Host/ })).toHaveAttribute('aria-sort', 'none');
  });

  it('writes a header change to the URL and drops the page', async () => {
    api();
    const user = userEvent.setup();
    const router = server('/o/acme/hosts?page=2', { total: 60 });

    await user.click(await screen.findByRole('button', { name: 'Zone' }));

    await waitFor(() => expect(router.state.location.search).toEqual({ sort: 'zone', order: 'asc' }));
    expect(query()).toEqual({ page: 1, per_page: 25, sort: 'zone', order: 'asc' });
  });

  it('leaves the keys out again for the default order', async () => {
    api();
    const user = userEvent.setup();
    const router = server('/o/acme/hosts?sort=zone&order=desc', { total: 60 });

    await user.click(await screen.findByRole('button', { name: 'Zone' }));

    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });

  it('writes a page change to the URL', async () => {
    api();
    const user = userEvent.setup();
    const router = server('/o/acme/hosts', { total: 60 });

    await user.click(await screen.findByRole('button', { name: 'Page 3' }));

    await waitFor(() => expect(router.state.location.search).toEqual({ page: 3 }));
    expect(query()).toEqual({ page: 3, per_page: 25 });
  });

  it('resets the page when resetKey changes', async () => {
    api();
    const user = userEvent.setup();
    const router = server('/o/acme/hosts?page=3&sort=zone&order=asc', { total: 100, withFilter: true });
    await waitFor(() => expect(query().page).toBe(3));
    const before = router.history.length;

    await user.click(screen.getByRole('button', { name: 'Filter' }));

    expect(query().page).toBe(1);
    await waitFor(() => expect(router.state.location.search).toEqual({ sort: 'zone', order: 'asc' }));
    expect(router.history.length).toBe(before);
  });

  it('clamps a page past the end', async () => {
    api();
    const router = server('/o/acme/hosts?page=9', { total: 30 });

    await waitFor(() => expect(router.state.location.search).toEqual({ page: 2 }));
    expect(router.history.length).toBe(1);
    expect(query()).toEqual({ page: 2, per_page: 25 });
    expect(screen.getByRole('button', { name: 'Page 2' })).toHaveAttribute('aria-current', 'page');
    expect(screen.queryByText('No hosts')).not.toBeInTheDocument();
  });

  it('clamps to page 1 when nothing matches', async () => {
    api();
    const router = server('/o/acme/hosts?page=4', { total: 0 });

    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });

  it('keeps the page while the total is unknown', async () => {
    api();
    const router = server('/o/acme/hosts?page=9', { total: undefined });

    await sleep(30);
    expect(router.state.location.search).toEqual({ page: 9 });
    expect(query().page).toBe(9);
  });

  it('keeps sort and page in the component with urlState false', async () => {
    api();
    const user = userEvent.setup();
    const router = server('/o/acme/hosts?page=2&sort=name&order=asc', { total: 60, urlState: false });

    await waitFor(() => expect(query()).toEqual({ page: 1, per_page: 25 }));
    await user.click(screen.getByRole('button', { name: 'Zone' }));
    await user.click(screen.getByRole('button', { name: 'Page 2' }));

    expect(query()).toEqual({ page: 2, per_page: 25, sort: 'zone', order: 'asc' });
    expect(router.state.location.search).toEqual({ page: 2, sort: 'name', order: 'asc' });
  });

  it('applies the saved page size and density', async () => {
    api({ page_size: 50, density: 'compact' });
    server('/o/acme/hosts', { total: 60 });

    await waitFor(() => expect(query()).toEqual({ page: 1, per_page: 50 }));
    expect(screen.getByTestId('density')).toHaveTextContent('compact');
  });

  it('saves a page-size change at once and goes back to page 1', async () => {
    api();
    const user = userEvent.setup();
    const router = server('/o/acme/hosts?page=2', { total: 60 });
    await screen.findByRole('button', { name: 'Page 2' });

    await user.click(screen.getByRole('combobox', { name: 'Rows per page' }));
    await user.click(screen.getByRole('option', { name: '50' }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ density: 'standard', page_size: 50, lists: {} });
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    expect(query()).toEqual({ page: 1, per_page: 50 });
  });
});

describe('useListView columns', () => {
  it('shows saved columns with the required one first and drops stale ids', async () => {
    api({ lists: { hosts: { columns: ['zone', 'gone', 'name', 'zone', 'os'] } } });
    server('/o/acme/hosts', { total: 2 });

    await waitFor(() => expect(headers()).toEqual(['Host', 'Zone', 'Operating system']));
  });

  it('saves two column changes within 400 ms as one PUT with the second list', async () => {
    api({ lists: { runs: { columns: ['run'] } } });
    const user = userEvent.setup();
    server('/o/acme/hosts', { total: 2 });
    await waitFor(() => expect(headers()).toEqual(['Host', 'Zone', 'Tags']));

    const dialog = await openColumns(user);
    await user.click(within(dialog).getByRole('checkbox', { name: /Zone/ }));
    await user.click(within(dialog).getByRole('checkbox', { name: /Operating system/ }));
    // Shown at once, saved later.
    expect(headers()).toEqual(['Host', 'Tags', 'Operating system']);
    expect(puts).toHaveLength(0);

    await waitFor(() => expect(puts).toHaveLength(1));
    await sleep(500);
    expect(puts).toHaveLength(1);
    expect(puts[0].lists).toEqual({ runs: { columns: ['run'] }, hosts: { columns: ['name', 'tags', 'os'] } });
    expect(headers()).toEqual(['Host', 'Tags', 'Operating system']);
  });

  it('loses neither a pending column save nor a page-size change made meanwhile', async () => {
    api();
    const user = userEvent.setup();
    server('/o/acme/hosts', { total: 60 });
    await waitFor(() => expect(headers()).toEqual(['Host', 'Zone', 'Tags']));

    const dialog = await openColumns(user);
    await user.click(within(dialog).getByRole('checkbox', { name: /Zone/ }));
    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('combobox', { name: 'Rows per page' }));
    await user.click(screen.getByRole('option', { name: '50' }));

    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[0].page_size).toBe(50);
    expect(puts[1]).toEqual({ density: 'standard', page_size: 50, lists: { hosts: { columns: ['name', 'tags'] } } });
    expect(query().per_page).toBe(50);
    expect(headers()).toEqual(['Host', 'Tags']);
  });

  it('removes the saved list on Restore defaults', async () => {
    api({ lists: { hosts: { columns: ['name', 'os'] }, runs: { columns: ['run'] } } });
    const user = userEvent.setup();
    server('/o/acme/hosts', { total: 2 });
    await waitFor(() => expect(headers()).toEqual(['Host', 'Operating system']));

    const dialog = await openColumns(user);
    await user.click(within(dialog).getByRole('button', { name: 'Restore defaults' }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].lists).toEqual({ runs: { columns: ['run'] } });
    expect(headers()).toEqual(['Host', 'Zone', 'Tags']);
  });

  it('saves a pending column change when the list goes away', async () => {
    api();
    const user = userEvent.setup();
    const router = server('/o/acme/hosts', { total: 2 });
    await waitFor(() => expect(headers()).toEqual(['Host', 'Zone', 'Tags']));

    const dialog = await openColumns(user);
    await user.click(within(dialog).getByRole('checkbox', { name: /Tags/ }));
    await router.navigate({ to: '/o/$org/elsewhere', params: { org: 'acme' } } as never);
    await screen.findByText('ELSEWHERE');

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].lists).toEqual({ hosts: { columns: ['name', 'zone'] } });
  });

  it('says so and shows the saved columns again when the save fails', async () => {
    api({}, () => new Response(JSON.stringify({ detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } }));
    const user = userEvent.setup();
    server('/o/acme/hosts', { total: 2 });
    await waitFor(() => expect(headers()).toEqual(['Host', 'Zone', 'Tags']));

    const dialog = await openColumns(user);
    await user.click(within(dialog).getByRole('checkbox', { name: /Zone/ }));
    expect(headers()).toEqual(['Host', 'Tags']);

    expect(await screen.findByText('Your list settings could not be saved')).toBeInTheDocument();
    await waitFor(() => expect(headers()).toEqual(['Host', 'Zone', 'Tags']));
  });

  it('refuses a column the registry does not know', () => {
    const bogus = [...COLUMNS, { id: 'bogus', label: 'Bogus', width: '1fr', defaultVisible: true, render: () => null }];
    expect(() => renderHook(() => useListView('hosts', bogus, { mode: 'client' }))).toThrow(/bogus.*registry/);
  });
});

describe('useListView in client mode', () => {
  it('sorts, then slices the page', async () => {
    api();
    renderRoutes([{ path: '/hosts', component: () => <ClientList rows={HOSTS} /> }], '/o/acme/hosts?sort=name&order=desc&page=2');

    await waitFor(() => expect(screen.getByTestId('rows')).toHaveTextContent('host5,host4,host3,host2,host1'));
    expect(screen.getByText('26–30 of 30 hosts')).toBeInTheDocument();
  });

  it('shows the rows in their own order without a sort', async () => {
    api();
    renderRoutes([{ path: '/hosts', component: () => <ClientList rows={HOSTS.slice(0, 3)} /> }], '/o/acme/hosts');

    await waitFor(() => expect(screen.getByTestId('rows')).toHaveTextContent('host1,host2,host3'));
  });

  it('clamps a page past the end without an empty page', async () => {
    api();
    const router = renderRoutes([{ path: '/hosts', component: () => <ClientList rows={HOSTS} /> }], '/o/acme/hosts?page=9');

    expect(await screen.findByTestId('rows')).toHaveTextContent('host26,host27,host28,host29,host30');
    await waitFor(() => expect(router.state.location.search).toEqual({ page: 2 }));
  });

  it('keeps the page while the rows are loading', async () => {
    api();
    const router = renderRoutes([{ path: '/hosts', component: () => <ClientList rows={undefined} /> }], '/o/acme/hosts?page=3');

    await screen.findByTestId('rows');
    await sleep(30);
    expect(router.state.location.search).toEqual({ page: 3 });
  });
});
