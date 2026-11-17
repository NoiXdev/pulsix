import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ColumnsMenu } from './ColumnsMenu.tsx';
import type { DataListColumn } from './types.ts';

interface Host {
  name: string;
}

const col = (id: string, label: string, extra: Partial<DataListColumn<Host>> = {}): DataListColumn<Host> => ({
  id,
  label,
  width: '1fr',
  defaultVisible: true,
  render: () => null,
  ...extra,
});

const COLUMNS: DataListColumn<Host>[] = [
  col('name', 'Host', { required: true }),
  col('zone', 'Zone'),
  col('tags', 'Tags'),
  col('state', '', { menuLabel: 'State' }),
  col('agent', 'Agent', { defaultVisible: false }),
];

function renderMenu(visible = ['name', 'zone', 'tags', 'state'], defaults = ['name', 'zone', 'tags', 'state']) {
  const onChange = vi.fn();
  render(<ColumnsMenu columns={COLUMNS} visibleColumns={visible} onVisibleColumnsChange={onChange} defaultColumns={defaults} />);
  return onChange;
}

async function open() {
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: 'Columns' }));
  return { user, dialog: screen.getByRole('dialog', { name: 'Columns' }) };
}

describe('ColumnsMenu', () => {
  it('lists every column in display order, the hidden ones after the shown ones', async () => {
    renderMenu(['name', 'tags', 'zone']);
    const { dialog } = await open();
    expect(within(dialog).getAllByRole('checkbox').map((c) => c.getAttribute('aria-label') ?? c.closest('label')?.textContent)).toEqual([
      expect.stringContaining('Host'),
      expect.stringContaining('Tags'),
      expect.stringContaining('Zone'),
      expect.stringContaining('State'),
      expect.stringContaining('Agent'),
    ]);
    expect(within(dialog).getByRole('checkbox', { name: /Tags/ })).toBeChecked();
    expect(within(dialog).getByRole('checkbox', { name: /Agent/ })).not.toBeChecked();
  });

  it('hides a column when it is unchecked', async () => {
    const onChange = renderMenu();
    const { user, dialog } = await open();
    await user.click(within(dialog).getByRole('checkbox', { name: /Zone/ }));
    expect(onChange).toHaveBeenCalledWith(['name', 'tags', 'state']);
  });

  it('shows a column when it is checked, at its place in the menu', async () => {
    const onChange = renderMenu();
    const { user, dialog } = await open();
    await user.click(within(dialog).getByRole('checkbox', { name: /Agent/ }));
    expect(onChange).toHaveBeenCalledWith(['name', 'zone', 'tags', 'state', 'agent']);
  });

  it('keeps the required column checked and disabled, with no move buttons', async () => {
    renderMenu();
    const { dialog } = await open();
    const host = within(dialog).getByRole('checkbox', { name: /Host/ });
    expect(host).toBeChecked();
    expect(host).toBeDisabled();
    expect(within(dialog).queryByRole('button', { name: 'Move Host down' })).not.toBeInTheDocument();
  });

  it('swaps a column with the one below on "Move down"', async () => {
    const onChange = renderMenu();
    const { user, dialog } = await open();
    await user.click(within(dialog).getByRole('button', { name: 'Move Zone down' }));
    expect(onChange).toHaveBeenCalledWith(['name', 'tags', 'zone', 'state']);
  });

  it('never moves a column above the required one', async () => {
    renderMenu();
    const { dialog } = await open();
    expect(within(dialog).getByRole('button', { name: 'Move Zone up' })).toBeDisabled();
    expect(within(dialog).getByRole('button', { name: 'Move Tags up' })).toBeEnabled();
  });

  it('names an icon column by its menu label', async () => {
    renderMenu();
    const { dialog } = await open();
    expect(within(dialog).getByRole('button', { name: 'Move State up' })).toBeInTheDocument();
  });

  it('restores the default columns', async () => {
    const onChange = renderMenu(['name', 'agent'], ['name', 'zone', 'tags', 'state']);
    const { user, dialog } = await open();
    await user.click(within(dialog).getByRole('button', { name: 'Restore defaults' }));
    expect(onChange).toHaveBeenCalledWith(['name', 'zone', 'tags', 'state']);
  });

  it('reorders by drag and drop, saving on the drop and not while dragging over', async () => {
    const onChange = renderMenu();
    const { dialog } = await open();
    const item = (label: RegExp) => within(dialog).getByRole('checkbox', { name: label }).closest('[draggable]') as HTMLElement;
    const state = item(/State/);
    const zone = item(/Zone/);
    fireEvent.dragStart(state);
    fireEvent.dragEnter(zone);
    fireEvent.dragOver(zone);
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.drop(zone);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(['name', 'state', 'zone', 'tags']);
  });

  it('does not let the required column be dragged or dropped on', async () => {
    const onChange = renderMenu();
    const { dialog } = await open();
    const host = within(dialog).getByRole('checkbox', { name: /Host/ }).closest('li') as HTMLElement;
    expect(host).toHaveAttribute('draggable', 'false');
    const tags = within(dialog).getByRole('checkbox', { name: /Tags/ }).closest('li') as HTMLElement;
    fireEvent.dragStart(tags);
    fireEvent.drop(host);
    expect(onChange).not.toHaveBeenCalled();
  });

  it('closes on Escape and gives focus back to the button', async () => {
    renderMenu();
    const { user } = await open();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: 'Columns' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Columns' })).toHaveFocus();
  });
});
