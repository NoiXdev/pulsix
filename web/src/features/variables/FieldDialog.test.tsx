import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { FieldDialog } from './FieldDialog.tsx';
import type { EditorVariant, FieldSource, SchemaField } from './types.ts';

const src: FieldSource = { kind: 'argument_spec', role: 'web', file: 'roles/web/meta/argument_specs.yml', line: 4 };

function open(field: SchemaField, variant: EditorVariant = 'template') {
  const onSave = vi.fn();
  const onRemove = vi.fn();
  const onClose = vi.fn();
  render(<FieldDialog field={field} path="fields[0]" variant={variant} onSave={onSave} onRemove={onRemove} onClose={onClose} />);
  return { onSave, onRemove, onClose, dialog: screen.getByRole('dialog', { name: `Field ${field.name}` }) };
}

describe('FieldDialog', () => {
  it('shows where a source field came from and keeps its name read-only', () => {
    const { dialog } = open({ name: 'port', type: 'int', origin: 'source', source: src });
    expect(within(dialog).getByLabelText('Name')).toHaveAttribute('readonly');
    expect(within(dialog).getByText('From argument spec of role web · roles/web/meta/argument_specs.yml:4')).toBeInTheDocument();
    expect(within(dialog).getByText('From playbook')).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Delete field' })).not.toBeInTheDocument();
  });

  it('lets a manual field be renamed and deleted', async () => {
    const user = userEvent.setup();
    const { dialog, onRemove } = open({ name: 'env', type: 'string' });
    expect(within(dialog).getByLabelText('Name')).not.toHaveAttribute('readonly');
    await user.click(within(dialog).getByRole('button', { name: 'Delete field' }));
    expect(onRemove).toHaveBeenCalledTimes(1);
  });

  it('says what the source says for an adjusted attribute and resets to it', async () => {
    const user = userEvent.setup();
    const { dialog, onSave } = open({
      name: 'port', type: 'int', default: 8080, label: 'Port', origin: 'adjusted', source: src, active: false,
      source_field: { name: 'port', type: 'int', default: 80, label: 'Port' },
    });
    expect(within(dialog).getByText('Adjusted')).toBeInTheDocument();
    expect(within(dialog).getByText('Source says: 80')).toBeInTheDocument();
    // Only the attributes that differ say so.
    expect(within(dialog).getAllByText(/^Source says:/)).toHaveLength(1);
    await user.click(within(dialog).getByRole('button', { name: 'Reset to source' }));
    expect(within(dialog).getByLabelText('Default')).toHaveValue('80');
    expect(within(dialog).queryByText('Source says: 80')).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(onSave).toHaveBeenCalledWith({ name: 'port', type: 'int', default: 80, label: 'Port', origin: 'source', source: src, active: false });
  });

  it('shows an edit of a source field as adjusted right away', async () => {
    const user = userEvent.setup();
    const { dialog, onSave } = open({ name: 'port', type: 'int', default: 80, origin: 'source', source: src });
    const dflt = within(dialog).getByLabelText('Default');
    await user.clear(dflt);
    await user.type(dflt, '8080');
    expect(within(dialog).getByText('Adjusted')).toBeInTheDocument();
    expect(within(dialog).getByText('Source says: 80')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(onSave).toHaveBeenCalledWith({
      name: 'port', type: 'int', default: 8080, origin: 'adjusted', source: src, source_field: { name: 'port', type: 'int', default: 80 },
    });
  });

  it('offers to remove a gone field or keep it as a manual one', async () => {
    const user = userEvent.setup();
    const { dialog, onRemove, onSave } = open({ name: 'old', type: 'string', origin: 'gone', source: src });
    expect(within(dialog).getByText('Gone from source')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }));
    expect(onRemove).toHaveBeenCalledTimes(1);

    await user.click(within(dialog).getByRole('button', { name: 'Keep as manual field' }));
    expect(within(dialog).getByText('Manual')).toBeInTheDocument();
    expect(within(dialog).getByLabelText('Name')).not.toHaveAttribute('readonly');
    await user.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(onSave).toHaveBeenCalledWith({ name: 'old', type: 'string' });
  });

  it('marks a variable the playbook already sets', () => {
    const { dialog } = open({ name: 'site', type: 'string', origin: 'source', source: { kind: 'marker', file: 'site.yml', line: 7 }, set_in_playbook: true });
    expect(within(dialog).getByText('Set in playbook')).toBeInTheDocument();
    expect(within(dialog).getByText('From inline marker · site.yml:7')).toBeInTheDocument();
  });

  it('does not save on cancel', async () => {
    const user = userEvent.setup();
    const { dialog, onSave, onClose } = open({ name: 'env', type: 'string' });
    await user.type(within(dialog).getByLabelText('Label'), 'Environment');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(onSave).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
  });

  it('saves on Enter without submitting a form around the editor', async () => {
    const user = userEvent.setup();
    const outer = vi.fn((e: { preventDefault: () => void }) => e.preventDefault());
    const onSave = vi.fn();
    render(
      <form onSubmit={outer}>
        <FieldDialog field={{ name: 'env', type: 'string' }} path="fields[0]" variant="template" onSave={onSave} onClose={() => {}} />
      </form>,
    );
    await user.type(screen.getByLabelText('Label'), 'Environment{enter}');
    expect(onSave).toHaveBeenCalledWith({ name: 'env', type: 'string', label: 'Environment' });
    expect(outer).not.toHaveBeenCalled();
  });
});
