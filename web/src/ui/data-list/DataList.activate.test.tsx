import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { DataList, type DataListColumn, type DataListProps, type RowAction } from './index.ts';

interface Token {
  id: string;
  name: string;
  role: string;
}

const TOKENS: Token[] = [
  { id: 't1', name: 'ci', role: 'operator' },
  { id: 't2', name: 'backup', role: 'viewer' },
];

const COLUMNS: DataListColumn<Token>[] = [
  { id: 'name', label: 'Name', width: 'minmax(0,1fr)', required: true, defaultVisible: true, render: (r) => <span>{r.name}</span> },
  { id: 'role', label: 'Role', width: '100px', defaultVisible: true, render: (r) => r.role },
];

function renderList(over: Partial<DataListProps<Token>> = {}) {
  const props: DataListProps<Token> = {
    label: 'Tokens',
    columns: COLUMNS,
    rows: TOKENS,
    rowKey: (r) => r.id,
    rowLabel: (r) => `${r.name} (${r.role})`,
    visibleColumns: ['name', 'role'],
    onVisibleColumnsChange: vi.fn(),
    defaultColumns: ['name', 'role'],
    density: 'standard',
    empty: 'No tokens yet.',
    ...over,
  };
  renderRoutes(
    [
      { path: '/tokens', component: () => <DataList {...props} /> },
      { path: '/runs', component: () => <div>runs page</div> },
    ],
    '/o/acme/tokens',
  );
}

function rowOf(name: string): HTMLElement {
  const row = screen.getAllByRole('row').find((r) => within(r).queryByText(name));
  if (!row) throw new Error(`no row ${name}`);
  return row;
}

const actions = (): RowAction[] => [{ id: 'revoke', label: 'Revoke', onSelect: vi.fn() }];

describe('DataList row activation', () => {
  it('makes the name cell one button named by the row label', async () => {
    renderList({ onRowActivate: vi.fn() });
    await screen.findByRole('table', { name: 'Tokens' });
    const button = screen.getByRole('button', { name: 'ci (operator)' });
    expect(button).toHaveAttribute('type', 'button');
    expect(within(rowOf('ci')).getAllByRole('button')).toHaveLength(1);
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });

  it('activates the row on a click', async () => {
    const onRowActivate = vi.fn();
    renderList({ onRowActivate });
    await screen.findByRole('table', { name: 'Tokens' });
    await userEvent.setup().click(screen.getByRole('button', { name: 'backup (viewer)' }));
    expect(onRowActivate).toHaveBeenCalledExactlyOnceWith(TOKENS[1]);
  });

  it('activates the row with Enter and with Space', async () => {
    const onRowActivate = vi.fn();
    renderList({ onRowActivate });
    await screen.findByRole('table', { name: 'Tokens' });
    const user = userEvent.setup();
    screen.getByRole('button', { name: 'ci (operator)' }).focus();
    await user.keyboard('{Enter}');
    expect(onRowActivate).toHaveBeenLastCalledWith(TOKENS[0]);
    await user.keyboard(' ');
    expect(onRowActivate).toHaveBeenCalledTimes(2);
  });

  it('leaves a row that is not activatable as plain text', async () => {
    renderList({ onRowActivate: vi.fn(), isRowActivatable: (r) => r.id !== 't2' });
    await screen.findByRole('table', { name: 'Tokens' });
    expect(within(rowOf('ci')).getByRole('button', { name: 'ci (operator)' })).toBeInTheDocument();
    expect(within(rowOf('backup')).queryByRole('button')).not.toBeInTheDocument();
  });

  it('lets a link in a cell navigate without activating the row', async () => {
    const onRowActivate = vi.fn();
    const runs: DataListColumn<Token> = {
      id: 'runs',
      label: 'Runs',
      width: '100px',
      defaultVisible: true,
      render: () => (
        <a href="/o/acme/runs" className="relative z-10" onClick={(e) => e.preventDefault()}>
          2 runs
        </a>
      ),
    };
    renderList({ onRowActivate, columns: [...COLUMNS, runs], visibleColumns: ['name', 'role', 'runs'] });
    await screen.findByRole('table', { name: 'Tokens' });
    await userEvent.setup().click(within(rowOf('ci')).getByRole('link', { name: '2 runs' }));
    expect(onRowActivate).not.toHaveBeenCalled();
  });

  it('lets a button in a cell fire on its own without activating the row', async () => {
    const onRowActivate = vi.fn();
    const onCopy = vi.fn();
    const copy: DataListColumn<Token> = {
      id: 'copy',
      label: 'Copy',
      width: '80px',
      defaultVisible: true,
      render: () => (
        <button type="button" className="relative z-10" onClick={onCopy}>
          Copy id
        </button>
      ),
    };
    renderList({ onRowActivate, columns: [...COLUMNS, copy], visibleColumns: ['name', 'role', 'copy'] });
    await screen.findByRole('table', { name: 'Tokens' });
    await userEvent.setup().click(within(rowOf('ci')).getByRole('button', { name: 'Copy id' }));
    expect(onCopy).toHaveBeenCalledOnce();
    expect(onRowActivate).not.toHaveBeenCalled();
  });

  it('keeps the context menu as it is, without link entries', async () => {
    const onRowActivate = vi.fn();
    renderList({ onRowActivate, rowActions: actions });
    await screen.findByRole('table', { name: 'Tokens' });
    expect(fireEvent.contextMenu(within(rowOf('ci')).getByText('operator'), { button: 2 })).toBe(false);
    const labels = within(screen.getByRole('menu'))
      .getAllByRole('menuitem')
      .map((i) => i.textContent);
    expect(labels).toEqual(['Revoke']);
    expect(onRowActivate).not.toHaveBeenCalled();
  });

  it('opens the menu with Shift+F10 on the row button and gives focus back on Escape', async () => {
    renderList({ onRowActivate: vi.fn(), rowActions: actions });
    await screen.findByRole('table', { name: 'Tokens' });
    const user = userEvent.setup();
    const button = screen.getByRole('button', { name: 'ci (operator)' });
    button.focus();
    await user.keyboard('{Shift>}{F10}{/Shift}');
    expect(screen.getByRole('menu')).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(button).toHaveFocus();
  });

  it('gives the row link precedence when both are set', async () => {
    const onRowActivate = vi.fn();
    renderList({ onRowActivate, rowLink: (r) => ({ to: '/o/$org/runs', params: { org: 'acme' }, label: `Open ${r.name}` }) });
    await screen.findByRole('table', { name: 'Tokens' });
    expect(screen.getByRole('link', { name: 'Open ci' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'ci (operator)' })).not.toBeInTheDocument();
  });
});
