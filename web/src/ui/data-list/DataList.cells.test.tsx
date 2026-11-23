import { describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { DataList, type DataListColumn, type DataListProps } from './index.ts';

interface Repo {
  id: string;
  name: string;
  error: string;
}

const REPOS: Repo[] = [
  { id: 'r1', name: 'infra', error: 'auth failed' },
  { id: 'r2', name: 'apps', error: '' },
];

const COLUMNS: DataListColumn<Repo>[] = [
  {
    id: 'name',
    label: 'Name',
    width: 'minmax(0,1fr)',
    required: true,
    defaultVisible: true,
    render: (r) => (
      <span>
        {r.name} <span>Managed</span>
      </span>
    ),
  },
  {
    id: 'sync',
    label: 'Sync',
    width: '100px',
    defaultVisible: true,
    render: (r) => (
      <span className="relative z-10" title={r.error}>
        sync of {r.name}
      </span>
    ),
  },
];

function renderList(over: Partial<DataListProps<Repo>> = {}) {
  const props: DataListProps<Repo> = {
    label: 'Repos',
    columns: COLUMNS,
    rows: REPOS,
    rowKey: (r) => r.id,
    rowLabel: (r) => r.name,
    visibleColumns: ['name', 'sync'],
    onVisibleColumnsChange: vi.fn(),
    defaultColumns: ['name', 'sync'],
    density: 'standard',
    empty: 'No repos yet.',
    ...over,
  };
  renderRoutes(
    [
      { path: '/repos', component: () => <DataList {...props} /> },
      { path: '/hosts/$id', component: () => <div>repo page</div> },
    ],
    '/o/acme/repos',
  );
}

const link = (r: Repo) => ({ to: '/o/$org/hosts/$id' as const, params: { org: 'acme', id: r.id }, label: `Open ${r.name}` });

function rowOf(name: string): HTMLElement {
  const row = screen.getAllByRole('row').find((r) => within(r).queryByText(`sync of ${name}`));
  if (!row) throw new Error(`no row ${name}`);
  return row;
}

describe('DataList name cell description', () => {
  it('describes the row button by the name cell\'s full text', async () => {
    renderList({ onRowActivate: vi.fn() });
    await screen.findByRole('table', { name: 'Repos' });
    const button = screen.getByRole('button', { name: 'infra' });
    expect(button).toHaveAccessibleDescription(expect.stringContaining('Managed'));
  });

  it('describes the row link by the name cell\'s full text', async () => {
    renderList({ rowLink: link });
    await screen.findByRole('table', { name: 'Repos' });
    const a = screen.getByRole('link', { name: 'Open infra' });
    expect(a).toHaveAccessibleDescription(expect.stringContaining('Managed'));
  });

  // A name cell that says nothing beyond the label would be read twice.
  const PLAIN: DataListColumn<Repo>[] = [{ ...COLUMNS[0], render: (r) => <span>{r.name}</span> }, COLUMNS[1]];

  it('leaves the row link undescribed when the name cell repeats its label', async () => {
    renderList({ columns: PLAIN, rowLink: (r) => ({ ...link(r), label: r.name }) });
    await screen.findByRole('table', { name: 'Repos' });
    const a = screen.getByRole('link', { name: 'infra' });
    expect(a).not.toHaveAttribute('aria-describedby');
    expect(a).toHaveAccessibleDescription('');
  });

  it('leaves the row button undescribed when the name cell repeats its label', async () => {
    renderList({ columns: PLAIN, onRowActivate: vi.fn() });
    await screen.findByRole('table', { name: 'Repos' });
    const button = screen.getByRole('button', { name: 'apps' });
    expect(button).not.toHaveAttribute('aria-describedby');
    expect(button).toHaveAccessibleDescription('');
  });

  it('gives every row its own description', async () => {
    renderList({ onRowActivate: vi.fn() });
    await screen.findByRole('table', { name: 'Repos' });
    expect(screen.getByRole('button', { name: 'apps' })).toHaveAccessibleDescription(expect.stringContaining('apps'));
  });
});

describe('DataList text raised above the row', () => {
  it('activates the row on a click on a raised element in another cell', async () => {
    const onRowActivate = vi.fn();
    renderList({ onRowActivate });
    await screen.findByRole('table', { name: 'Repos' });
    await userEvent.setup().click(within(rowOf('infra')).getByText('sync of infra'));
    expect(onRowActivate).toHaveBeenCalledExactlyOnceWith(REPOS[0]);
  });

  it('follows the row link on a click on a raised element in another cell', async () => {
    renderList({ rowLink: link });
    await screen.findByRole('table', { name: 'Repos' });
    await userEvent.setup().click(within(rowOf('apps')).getByText('sync of apps'));
    expect(await screen.findByText('repo page')).toBeInTheDocument();
  });

  it('leaves a modified click on a raised element alone', async () => {
    const onRowActivate = vi.fn();
    renderList({ onRowActivate });
    await screen.findByRole('table', { name: 'Repos' });
    const user = userEvent.setup();
    await user.keyboard('{Control>}');
    await user.click(within(rowOf('infra')).getByText('sync of infra'));
    await user.keyboard('{/Control}');
    expect(onRowActivate).not.toHaveBeenCalled();
  });

  it('activates the row once on a click inside its own button', async () => {
    const onRowActivate = vi.fn();
    renderList({ onRowActivate });
    await screen.findByRole('table', { name: 'Repos' });
    await userEvent.setup().click(within(rowOf('infra')).getByText('Managed'));
    expect(onRowActivate).toHaveBeenCalledOnce();
  });
});
