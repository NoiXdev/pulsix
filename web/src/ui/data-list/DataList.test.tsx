import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { DataList, type DataListColumn, type DataListProps, type RowAction, type SortState } from './index.ts';

interface Host {
  id: string;
  name: string;
  zone: string;
}

const HOSTS: Host[] = [
  { id: 'h1', name: 'web-01', zone: 'fra-1' },
  { id: 'h2', name: 'db-01', zone: 'ber-1' },
];

const COLUMNS: DataListColumn<Host>[] = [
  { id: 'name', label: 'Host', width: 'minmax(0,1fr)', sortKey: 'name', required: true, defaultVisible: true, render: (h) => <span>{h.name}</span> },
  { id: 'zone', label: 'Zone', width: '100px', sortKey: 'zone', defaultVisible: true, render: (h) => h.zone },
  { id: 'tags', label: 'Tags', width: '1fr', defaultVisible: true, render: () => 'no tags' },
  { id: 'agent', label: 'Agent', width: '80px', defaultVisible: false, align: 'end', render: () => '0.1.0' },
];

function renderList(over: Partial<DataListProps<Host>> = {}) {
  const props: DataListProps<Host> = {
    label: 'Hosts',
    columns: COLUMNS,
    rows: HOSTS,
    rowKey: (h) => h.id,
    visibleColumns: ['name', 'zone', 'tags'],
    onVisibleColumnsChange: vi.fn(),
    defaultColumns: ['name', 'zone', 'tags'],
    density: 'standard',
    empty: 'No hosts yet.',
    ...over,
  };
  renderRoutes(
    [
      { path: '/hosts', component: () => <DataList {...props} /> },
      { path: '/hosts/$id', component: () => <div>detail page</div> },
    ],
    '/o/acme/hosts',
  );
  return props;
}

const link = (h: Host) => ({ to: '/o/$org/hosts/$id' as const, params: { org: 'acme', id: h.id }, label: `Open ${h.name}` });

function actions(onRestart = vi.fn(), onDelete = vi.fn()): (h: Host) => RowAction[] {
  return () => [
    { id: 'restart', label: 'Restart', onSelect: onRestart },
    { kind: 'link', id: 'log', label: 'Download log', href: '/log.txt', download: 'log.txt' },
    { id: 'delete', label: 'Delete', onSelect: onDelete, destructive: true },
  ];
}

async function table() {
  return screen.findByRole('table', { name: 'Hosts' });
}

function rowOf(name: string): HTMLElement {
  const row = screen.getAllByRole('row').find((r) => within(r).queryByText(name));
  if (!row) throw new Error(`no row ${name}`);
  return row;
}

const menuLabels = () => within(screen.getByRole('menu')).getAllByRole('menuitem').map((i) => i.textContent);

describe('DataList columns', () => {
  it('renders only the visible columns, in the order given', async () => {
    renderList({ visibleColumns: ['zone', 'name'] });
    await table();
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Zone', 'Host']);
    expect(within(rowOf('web-01')).getAllByRole('cell').map((c) => c.textContent)).toEqual(['fra-1', 'web-01']);
    expect(screen.queryByText('no tags')).not.toBeInTheDocument();
  });

  it('skips ids it has no column for', async () => {
    renderList({ visibleColumns: ['name', 'gone', 'zone'] });
    await table();
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Host', 'Zone']);
  });

  it('lays rows out on the visible columns\' tracks, with one more for the row menu', async () => {
    renderList({ rowActions: actions() });
    await table();
    expect(rowOf('web-01')).toHaveStyle({ gridTemplateColumns: 'minmax(0,1fr) 100px 1fr 40px' });
    expect(screen.getAllByRole('columnheader')).toHaveLength(4);
  });

  it('passes the density to the cells', async () => {
    const render = vi.fn(() => null);
    renderList({ density: 'comfortable', columns: [{ ...COLUMNS[0], render }], visibleColumns: ['name'] });
    await table();
    expect(render).toHaveBeenCalledWith(HOSTS[0], { density: 'comfortable' });
  });

  it('aligns an end column to the end', async () => {
    renderList({ visibleColumns: ['name', 'agent'] });
    await table();
    expect(screen.getByRole('columnheader', { name: 'Agent' })).toHaveClass('text-right');
  });
});

describe('DataList sorting', () => {
  function header(name: string) {
    return screen.getByRole('columnheader', { name });
  }

  it('sorts ascending on the first click of an unsorted column', async () => {
    const onSortChange = vi.fn();
    renderList({ sort: null, onSortChange });
    await table();
    expect(header('Host')).toHaveAttribute('aria-sort', 'none');
    await userEvent.setup().click(within(header('Host')).getByRole('button', { name: 'Host' }));
    expect(onSortChange).toHaveBeenCalledWith({ key: 'name', order: 'asc' });
  });

  it('turns ascending into descending', async () => {
    const onSortChange = vi.fn();
    renderList({ sort: { key: 'name', order: 'asc' }, onSortChange });
    await table();
    expect(header('Host')).toHaveAttribute('aria-sort', 'ascending');
    await userEvent.setup().click(within(header('Host')).getByRole('button'));
    expect(onSortChange).toHaveBeenCalledWith({ key: 'name', order: 'desc' });
  });

  it('turns descending off', async () => {
    const onSortChange = vi.fn();
    renderList({ sort: { key: 'name', order: 'desc' }, onSortChange });
    await table();
    expect(header('Host')).toHaveAttribute('aria-sort', 'descending');
    expect(header('Zone')).toHaveAttribute('aria-sort', 'none');
    await userEvent.setup().click(within(header('Host')).getByRole('button'));
    expect(onSortChange).toHaveBeenCalledWith(null);
  });

  it('starts another column at ascending', async () => {
    const onSortChange = vi.fn<(s: SortState | null) => void>();
    renderList({ sort: { key: 'name', order: 'desc' }, onSortChange });
    await table();
    await userEvent.setup().click(within(header('Zone')).getByRole('button'));
    expect(onSortChange).toHaveBeenCalledWith({ key: 'zone', order: 'asc' });
  });

  it('gives a column without a sort key no button and no aria-sort', async () => {
    renderList({ sort: null, onSortChange: vi.fn() });
    await table();
    expect(within(header('Tags')).queryByRole('button')).not.toBeInTheDocument();
    expect(header('Tags')).not.toHaveAttribute('aria-sort');
  });
});

describe('DataList rows', () => {
  it('makes a linked row one link named by its label', async () => {
    renderList({ rowLink: link });
    await table();
    const a = screen.getByRole('link', { name: 'Open web-01' });
    expect(a).toHaveAttribute('href', '/o/acme/hosts/h1');
    expect(within(rowOf('web-01')).getAllByRole('link')).toHaveLength(1);
  });

  it('opens the row\'s page on a click', async () => {
    renderList({ rowLink: link });
    await table();
    await userEvent.setup().click(screen.getByRole('link', { name: 'Open db-01' }));
    expect(await screen.findByText('detail page')).toBeInTheDocument();
  });

  it('shows the empty state when there are no rows', async () => {
    renderList({ rows: [] });
    await table();
    expect(screen.getByText('No hosts yet.')).toBeInTheDocument();
  });

  it('shows a loading state instead of the empty one while loading', async () => {
    renderList({ rows: [], loading: true });
    expect(await table()).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    expect(screen.queryByText('No hosts yet.')).not.toBeInTheDocument();
  });

  it('renders the toolbar beside the Columns button', async () => {
    renderList({ toolbar: <input aria-label="Search hosts" /> });
    await table();
    expect(screen.getByRole('textbox', { name: 'Search hosts' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Columns' })).toBeInTheDocument();
  });

  it('shows the pager only when paging is given', async () => {
    renderList({
      paging: { page: 1, perPage: 25, total: 2, countLabel: (n) => `${n} hosts`, onPageChange: vi.fn(), onPerPageChange: vi.fn() },
    });
    await table();
    expect(screen.getByText('1–2 of 2 hosts')).toBeInTheDocument();
  });
});

describe('DataList row menus', () => {
  it('gives each row a "…" menu with the row\'s actions', async () => {
    const onRestart = vi.fn();
    renderList({ rowLink: link, rowActions: actions(onRestart) });
    await table();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Actions for Open web-01' }));
    expect(menuLabels()).toEqual(['Restart', 'Download log', 'Delete']);
    await user.click(screen.getByRole('menuitem', { name: 'Restart' }));
    expect(onRestart).toHaveBeenCalledOnce();
    expect(screen.queryByText('detail page')).not.toBeInTheDocument();
  });

  it('names the "…" button by rowLabel when there is one', async () => {
    renderList({ rowActions: actions(), rowLabel: (h) => h.name });
    await table();
    expect(screen.getByRole('button', { name: 'Actions for web-01' })).toBeInTheDocument();
  });

  it('colours a destructive action', async () => {
    renderList({ rowActions: actions() });
    await table();
    const user = userEvent.setup();
    await user.click(within(rowOf('web-01')).getByRole('button', { name: /Actions/ }));
    expect(screen.getByRole('menuitem', { name: 'Delete' })).toHaveClass('text-destructive');
    expect(screen.getByRole('menuitem', { name: 'Restart' })).not.toHaveClass('text-destructive');
  });

  it('opens the same actions on a right-click, at the pointer', async () => {
    const onDelete = vi.fn();
    renderList({ rowActions: actions(vi.fn(), onDelete) });
    await table();
    const user = userEvent.setup();
    await user.click(within(rowOf('web-01')).getByRole('button', { name: /Actions/ }));
    const fromButton = menuLabels();
    await user.keyboard('{Escape}');

    fireEvent.contextMenu(within(rowOf('web-01')).getByText('fra-1'), { clientX: 200, clientY: 300 });
    const menu = screen.getByRole('menu');
    expect(menuLabels()).toEqual(fromButton);
    expect(menu).toHaveStyle({ left: '200px', top: '300px' });
    await user.click(within(menu).getByRole('menuitem', { name: 'Delete' }));
    expect(onDelete).toHaveBeenCalledOnce();
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('adds "Open in new tab" and "Copy link" on a linked row', async () => {
    renderList({ rowLink: link, rowActions: actions() });
    await table();
    fireEvent.contextMenu(within(rowOf('web-01')).getByText('fra-1'));
    expect(menuLabels()).toEqual(['Open in new tab', 'Copy link', 'Restart', 'Download log', 'Delete']);
  });

  it('opens the row\'s page in a new tab', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    renderList({ rowLink: link, rowActions: actions() });
    await table();
    fireEvent.contextMenu(within(rowOf('web-01')).getByText('fra-1'));
    await userEvent.setup().click(screen.getByRole('menuitem', { name: 'Open in new tab' }));
    expect(open).toHaveBeenCalledWith('/o/acme/hosts/h1', '_blank', 'noopener,noreferrer');
    open.mockRestore();
  });

  it('copies the row\'s full link', async () => {
    const user = userEvent.setup();
    renderList({ rowLink: link, rowActions: actions() });
    await table();
    fireEvent.contextMenu(within(rowOf('db-01')).getByText('ber-1'));
    await user.click(screen.getByRole('menuitem', { name: 'Copy link' }));
    expect(await navigator.clipboard.readText()).toBe('http://localhost:3000/o/acme/hosts/h2');
    expect(await screen.findByText('Link copied')).toBeInTheDocument();
  });

  it('opens the menu with Shift+F10 on a focused row and gives focus back on Escape', async () => {
    renderList({ rowLink: link, rowActions: actions() });
    await table();
    const user = userEvent.setup();
    const a = screen.getByRole('link', { name: 'Open web-01' });
    a.focus();
    await user.keyboard('{Shift>}{F10}{/Shift}');
    expect(screen.getByRole('menu')).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'Open in new tab' })).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(a).toHaveFocus();
  });

  it('opens the menu with the ContextMenu key', async () => {
    renderList({ rowLink: link, rowActions: actions() });
    await table();
    const a = screen.getByRole('link', { name: 'Open db-01' });
    a.focus();
    await userEvent.setup().keyboard('{ContextMenu}');
    expect(menuLabels()).toContain('Copy link');
    expect(rowOf('db-01')).toHaveAttribute('data-menu-open');
  });

  it('gives a row without actions neither menu', async () => {
    renderList({ rowLink: link, rowActions: () => [] });
    await table();
    expect(within(rowOf('web-01')).queryByRole('button')).not.toBeInTheDocument();
    // Not prevented: the browser's own menu shows instead.
    expect(fireEvent.contextMenu(within(rowOf('web-01')).getByText('fra-1'))).toBe(true);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('ignores the context-menu keys pressed inside the open "…" menu', async () => {
    renderList({ rowLink: link, rowActions: actions() });
    await table();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Actions for Open web-01' }));
    await user.keyboard('{Shift>}{F10}{/Shift}');
    expect(screen.getAllByRole('menu')).toHaveLength(1);
    expect(menuLabels()).toEqual(['Restart', 'Download log', 'Delete']);
  });
});
