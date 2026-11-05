import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { pickOption } from '../../test/pickOption.ts';
import type { VarSchema } from './types.ts';
import { EditorHarness } from './EditorHarness.tsx';
import { schemaNow, dialog, subField, done, src } from './editorTestKit.ts';

describe('VariableEditor — sub-fields inline', () => {
  const vhosts = (origin?: 'source' | 'adjusted'): VarSchema => ({
    fields: [
      {
        name: 'vhosts', type: 'repeater', fields: [{ name: 'domain', type: 'string' }],
        ...(origin ? { origin, source: src } : {}),
        ...(origin === 'adjusted' ? { source_field: { name: 'vhosts', type: 'repeater', fields: [{ name: 'domain', type: 'string' }] } } : {}),
      },
    ],
  });

  it('expands a repeater and edits its sub-fields in the row', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={vhosts()} />);
    expect(screen.queryByRole('group', { name: 'Sub-field domain' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    await user.click(within(subField('domain')).getByLabelText('Required'));
    await user.click(screen.getByRole('button', { name: 'Add sub-field' }));
    await pickOption(within(subField('field_2')), 'Type', { value: 'secret' });
    expect(schemaNow().fields[0].fields).toEqual([{ name: 'domain', type: 'string', required: true }, { name: 'field_2', type: 'secret' }]);
  });

  it('makes a source field adjusted when one of its sub-fields is edited', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={vhosts('source')} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    await user.click(within(subField('domain')).getByLabelText('Required'));
    const f = schemaNow().fields[0];
    expect(f.origin).toBe('adjusted');
    expect(f.source_field).toEqual({ name: 'vhosts', type: 'repeater', fields: [{ name: 'domain', type: 'string' }] });
    expect(f.fields).toEqual([{ name: 'domain', type: 'string', required: true }]);
  });

  it('offers no new sub-field on a source field until it is adjusted', async () => {
    const user = userEvent.setup();
    const { unmount } = render(<EditorHarness variant="template" initial={vhosts('source')} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(screen.getByRole('button', { name: 'Add sub-field' })).toBeDisabled();
    unmount();
    render(<EditorHarness variant="template" initial={vhosts('adjusted')} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(screen.getByRole('button', { name: 'Add sub-field' })).toBeEnabled();
  });

  it('edits the attributes of a sub-field in its own dialog', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={{ fields: [{ name: 'auth', type: 'object', fields: [{ name: 'mode', type: 'enum', options: ['a'] }] }] }} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of auth' }));
    await user.click(within(subField('mode')).getByRole('button', { name: 'Edit mode' }));
    const options = within(dialog('mode')).getByLabelText('Options (one per line)');
    await user.clear(options);
    await user.type(options, 'a{enter}b');
    // A sub-field has no scope and no activation of its own.
    expect(within(dialog('mode')).queryByText('Scope')).not.toBeInTheDocument();
    await done('mode');
    expect(schemaNow().fields[0].fields).toEqual([{ name: 'mode', type: 'enum', options: ['a', 'b'] }]);
  });

  it('shows a repeater/object .fields problem with its sub-fields', async () => {
    const user = userEvent.setup();
    render(
      <EditorHarness
        initial={vhosts()}
        problems={[{ path: 'fields[0].fields', message: 'schemas nest at most 4 levels deep' }]}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(screen.getByText('schemas nest at most 4 levels deep')).toBeInTheDocument();
  });
});

describe('VariableEditor — expand control', () => {
  it('points aria-controls at the sub-field row only while it exists', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={{ fields: [{ name: 'vhosts', type: 'repeater', fields: [{ name: 'domain', type: 'string' }] }] }} />);
    const toggle = screen.getByRole('button', { name: 'Sub-fields of vhosts' });
    expect(toggle).not.toHaveAttribute('aria-controls');
    await user.click(toggle);
    const id = toggle.getAttribute('aria-controls');
    expect(id).toBeTruthy();
    expect(document.getElementById(id as string)).toContainElement(subField('domain'));
    await user.click(toggle);
    expect(toggle).not.toHaveAttribute('aria-controls');
  });
});

describe('VariableEditor — source sub-field names', () => {
  const spec = (): VarSchema['fields'][number] => ({
    name: 'vhosts', type: 'repeater', origin: 'source', source: src,
    fields: [{ name: 'domain', type: 'string' }, { name: 'ssl', type: 'object', fields: [{ name: 'cert', type: 'string' }] }],
  });
  const adjusted = (): VarSchema => {
    const source = spec();
    return {
      fields: [
        {
          ...source, origin: 'adjusted', source_field: { name: 'vhosts', type: 'repeater', fields: source.fields },
          fields: [...(source.fields ?? []), { name: 'extra', type: 'string' }],
        },
      ],
    };
  };
  const nameOf = (name: string) => within(subField(name)).getByLabelText('Name');

  it('locks the sub-field names of a source field', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [spec()] }} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(nameOf('domain')).toHaveAttribute('readonly');
    expect(nameOf('ssl')).toHaveAttribute('readonly');
  });

  it('locks them in the sub-field dialog, and the names below it', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [spec()] }} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    await user.click(within(subField('ssl')).getByRole('button', { name: 'Edit ssl' }));
    const names = within(dialog('ssl')).getAllByLabelText('Name');
    // The dialog's own name, then the one of its sub-field.
    expect(names).toHaveLength(2);
    for (const name of names) expect(name).toHaveAttribute('readonly');
  });

  it('keeps source sub-field names locked after the field turned adjusted, but not a manual one', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={adjusted()} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(nameOf('domain')).toHaveAttribute('readonly');
    expect(nameOf('ssl')).toHaveAttribute('readonly');
    expect(nameOf('extra')).not.toHaveAttribute('readonly');
    await user.click(within(subField('extra')).getByRole('button', { name: 'Edit extra' }));
    expect(within(dialog('extra')).getByLabelText('Name')).not.toHaveAttribute('readonly');
  });

  it('keeps them locked when a sub-field of a source field is edited', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [spec()] }} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    await user.click(within(subField('domain')).getByLabelText('Required'));
    expect(schemaNow().fields[0].origin).toBe('adjusted');
    expect(nameOf('domain')).toHaveAttribute('readonly');
  });

  // Deleting a source sub-field breaks the role's contract as much as
  // renaming it (Task 11 review): refused the same way.
  it('refuses deleting a source sub-field, in the row and in its dialog', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [spec()] }} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(within(subField('domain')).getByRole('button', { name: 'Remove domain' })).toBeDisabled();
    await user.click(within(subField('ssl')).getByRole('button', { name: 'Edit ssl' }));
    expect(within(dialog('ssl')).queryByRole('button', { name: 'Delete field' })).not.toBeInTheDocument();
    expect(within(dialog('ssl')).getByRole('button', { name: 'Remove cert' })).toBeDisabled();
  });

  it('lets a person delete the sub-fields they added to an adjusted field, not the source ones', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={adjusted()} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(within(subField('domain')).getByRole('button', { name: 'Remove domain' })).toBeDisabled();
    await user.click(within(subField('extra')).getByRole('button', { name: 'Remove extra' }));
    expect(schemaNow().fields[0].fields?.map((f) => f.name)).toEqual(['domain', 'ssl']);
  });

  it('leaves the sub-field names of a manual field free', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [{ name: 'hosts', type: 'repeater', fields: [{ name: 'fqdn', type: 'string' }] }] }} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of hosts' }));
    expect(nameOf('fqdn')).not.toHaveAttribute('readonly');
  });
});
