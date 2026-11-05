import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { pickOption } from '../../test/pickOption.ts';
import { VariableEditor } from './VariableEditor.tsx';
import { EditorHarness } from './EditorHarness.tsx';
import { schemaNow, dialog, subField, dataRows, openField, done, src } from './editorTestKit.ts';

describe('VariableEditor — the field list', () => {
  it('shows one row per top-level field with the six columns', () => {
    render(
      <EditorHarness
        variant="template"
        initial={{
          fields: [
            { name: 'port', type: 'int', required: true, default: 80, origin: 'source', source: src },
            { name: 'debug', type: 'bool', active: false },
          ],
        }}
      />,
    );
    const headers = within(screen.getByRole('table', { name: 'Fields' })).getAllByRole('columnheader').map((h) => h.textContent);
    // The six columns, then the row's order buttons.
    expect(headers).toEqual(['Name', 'Type', 'Required', 'Default', 'Origin', 'Active', 'Order']);
    const rows = dataRows();
    expect(rows).toHaveLength(2);
    const port = within(rows[0]).getAllByRole('cell');
    expect(port[0]).toHaveTextContent('port');
    expect(port[1]).toHaveTextContent('int');
    expect(within(port[2]).getByLabelText('required')).toBeInTheDocument();
    expect(port[3]).toHaveTextContent('80');
    expect(port[4]).toHaveTextContent('From playbook');
    expect(within(port[5]).getByRole('switch', { name: 'port active' })).toBeChecked();
    expect(within(rows[1]).getByRole('switch', { name: 'debug active' })).not.toBeChecked();
  });

  it('marks the field list as a card, so the layout check covers it', () => {
    render(<EditorHarness variant="group" initial={{ fields: [{ name: 'env', type: 'string' }] }} />);
    // web/e2e/layout.ts expectNoClippedCards checks every [data-card].
    expect(screen.getByRole('table', { name: 'Fields' }).closest('[data-card]')).not.toBeNull();
  });

  it('has no origin column outside job templates', () => {
    render(<EditorHarness variant="group" initial={{ fields: [{ name: 'env', type: 'string' }] }} />);
    expect(screen.queryByRole('columnheader', { name: 'Origin' })).not.toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Active' })).toBeInTheDocument();
  });

  it('switches a field inactive and active again', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={{ fields: [{ name: 'env', type: 'string' }] }} />);
    await user.click(screen.getByRole('switch', { name: 'env active' }));
    expect(schemaNow().fields[0]).toEqual({ name: 'env', type: 'string', active: false });
    await user.click(screen.getByRole('switch', { name: 'env active' }));
    expect(schemaNow().fields[0]).toEqual({ name: 'env', type: 'string' });
  });

  it('keeps a source field locked when it is only switched inactive', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [{ name: 'port', type: 'int', origin: 'source', source: src }] }} />);
    await user.click(screen.getByRole('switch', { name: 'port active' }));
    expect(schemaNow().fields[0]).toEqual({ name: 'port', type: 'int', origin: 'source', source: src, active: false });
  });

  it('shows the four origins on a job template', () => {
    render(
      <EditorHarness
        variant="template"
        initial={{
          fields: [
            { name: 'a', type: 'string', origin: 'source', source: src },
            { name: 'b', type: 'string', origin: 'adjusted', source: src, source_field: { name: 'b', type: 'int' } },
            { name: 'c', type: 'string' },
            { name: 'd', type: 'string', origin: 'gone', source: src },
          ],
        }}
      />,
    );
    expect(dataRows().map((r) => within(r).getAllByRole('cell')[4].textContent)).toEqual(['From playbook', 'Adjusted', 'Manual', 'Gone from source']);
  });

  it('adds a manual field through the field dialog', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [{ name: 'port', type: 'int', origin: 'source', source: src }] }} />);
    await user.click(screen.getByRole('button', { name: 'Add field' }));
    const name = within(dialog('field_2')).getByLabelText('Name');
    expect(name).not.toHaveAttribute('readonly');
    await user.clear(name);
    await user.type(name, 'region');
    await done('region');
    expect(schemaNow().fields).toEqual([
      { name: 'port', type: 'int', origin: 'source', source: src },
      { name: 'region', type: 'string' },
    ]);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('adds nothing when the new field dialog is cancelled', async () => {
    const user = userEvent.setup();
    render(<EditorHarness />);
    await user.click(screen.getByRole('button', { name: 'Add field' }));
    await user.click(within(dialog('field_1')).getByRole('button', { name: 'Cancel' }));
    expect(schemaNow().fields).toEqual([]);
  });

  it('makes a source field adjusted when its dialog changes it', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [{ name: 'port', type: 'int', default: 80, origin: 'source', source: src }] }} />);
    await openField('port');
    await user.type(within(dialog('port')).getByLabelText('Label'), 'HTTP port');
    await done('port');
    expect(schemaNow().fields[0]).toEqual({
      name: 'port', type: 'int', default: 80, label: 'HTTP port', origin: 'adjusted', source: src,
      source_field: { name: 'port', type: 'int', default: 80 },
    });
  });
});

describe('VariableEditor — variants', () => {
  it('asks for scope on a group and previews the assignment form', async () => {
    const user = userEvent.setup();
    render(
      <EditorHarness
        variant="group"
        initial={{
          fields: [
            { name: 'env', type: 'string' },
            { name: 'php_default', type: 'enum', options: ['8.3'], overridable: true },
            { name: 'domain', type: 'string', scope: 'host' },
          ],
        }}
      />,
    );
    const preview = screen.getByRole('region', { name: 'Assignment form preview' });
    expect(within(preview).getByLabelText('php_default')).toBeInTheDocument();
    expect(within(preview).getByLabelText('domain')).toBeInTheDocument();
    expect(within(preview).queryByLabelText('env')).not.toBeInTheDocument();

    await openField('env');
    await user.click(within(dialog('env')).getByLabelText(/Host \(asked/));
    expect(within(dialog('env')).queryByLabelText('Hosts may override')).not.toBeInTheDocument();
    await done('env');
    expect(schemaNow().fields[0]).toEqual({ name: 'env', type: 'string', scope: 'host' });
  });

  it('asks for no scope and shows no preview on a job template', async () => {
    render(<EditorHarness variant="template" initial={{ fields: [{ name: 'env', type: 'string' }] }} />);
    expect(screen.queryByRole('region', { name: 'Assignment form preview' })).not.toBeInTheDocument();
    await openField('env');
    expect(within(dialog('env')).queryByText('Scope')).not.toBeInTheDocument();
  });

  it('offers no secret type on a shared definition', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="shared" initial={{ fields: [{ name: 'region', type: 'string' }] }} />);
    await openField('region');
    await user.click(within(dialog('region')).getByLabelText('Type'));
    expect(within(dialog('region')).queryByRole('option', { name: 'secret' })).toBeNull();
    expect(within(dialog('region')).getByRole('option', { name: 'string' })).toBeInTheDocument();
  });

  it('only shows the fields when read-only', () => {
    render(<VariableEditor readOnly variant="template" value={{ fields: [{ name: 'env', type: 'string' }] }} onChange={() => {}} />);
    expect(screen.queryByRole('button', { name: 'Add field' })).not.toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'env active' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Move env down' })).toBeDisabled();
  });

  it('a viewer opens a field and sees its attributes without editing them', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<VariableEditor readOnly variant="group" value={{ fields: [{ name: 'env', type: 'string', required: true }] }} onChange={onChange} />);
    await openField('env');
    const field = dialog('env');
    expect(within(field).getByLabelText('Name')).toHaveValue('env');
    expect(within(field).getByLabelText('Name')).toBeDisabled();
    expect(within(field).getByLabelText('Required')).toBeChecked();
    expect(within(field).queryByRole('button', { name: 'Done' })).not.toBeInTheDocument();
    expect(within(field).queryByRole('button', { name: /delete/i })).not.toBeInTheDocument();
    await user.click(within(field).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("a viewer can change none of an opened field's inputs", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const fields = [
      { name: 'port', type: 'int' as const, required: true, min: 1, max: 65535, default: 8080, description: 'Listen port' },
      { name: 'level', type: 'enum' as const, options: ['a', 'b'] },
      { name: 'hosts', type: 'repeater' as const, fields: [{ name: 'addr', type: 'string' as const }] },
    ];
    render(<VariableEditor readOnly variant="group" value={{ fields }} onChange={onChange} />);
    const inputs = ['textbox', 'combobox', 'checkbox', 'switch', 'spinbutton'] as const;
    const enabledButtons: string[] = [];
    for (const { name } of fields) {
      await openField(name);
      const field = dialog(name);
      for (const role of inputs) {
        for (const input of within(field).queryAllByRole(role)) {
          // A disabled fieldset disables an input without an attribute of its own.
          expect(input.hasAttribute('readonly') || input.matches(':disabled'), `${name}: ${role} ${input.id}`).toBe(true);
        }
      }
      for (const button of within(field).queryAllByRole('button')) {
        if (!button.matches(':disabled')) enabledButtons.push(`${name}: ${button.textContent}`);
      }
      // Typing into the name changes nothing, whether the browser or the library refuses it.
      const nameInput = within(field).getByLabelText('Name');
      await user.type(nameInput, 'x');
      expect(nameInput).toHaveValue(name);
      await user.click(within(field).getByRole('button', { name: 'Cancel' }));
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    }
    expect(enabledButtons).toEqual(['port: Cancel', 'level: Cancel', 'hosts: Cancel']);
    expect(onChange).not.toHaveBeenCalled();
  });
});

describe('VariableEditor — migrated builder behaviour', () => {
  it('edits name, type and required of a field', async () => {
    const user = userEvent.setup();
    render(<EditorHarness />);
    await user.click(screen.getByRole('button', { name: 'Add field' }));
    const name = within(dialog('field_1')).getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'vhosts');
    await pickOption(within(dialog('vhosts')), 'Type', { value: 'repeater' });
    await user.click(within(dialog('vhosts')).getByLabelText('Required'));
    await done('vhosts');
    expect(schemaNow()).toEqual({ fields: [{ name: 'vhosts', type: 'repeater', required: true, fields: [{ name: 'value', type: 'string' }] }] });
  });

  it('edits enum options, bounds, pattern and default', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={{ fields: [{ name: 'php', type: 'enum', options: ['8.3'] }, { name: 'domain', type: 'string' }] }} />);
    await openField('php');
    const options = within(dialog('php')).getByLabelText('Options (one per line)');
    await user.clear(options);
    await user.type(options, '8.3{enter}8.4');
    await pickOption(within(dialog('php')), 'Default', { value: '8.4' });
    await done('php');
    expect(schemaNow().fields[0]).toEqual({ name: 'php', type: 'enum', options: ['8.3', '8.4'], default: '8.4' });

    await openField('domain');
    // paste, not type: user-event reads "[" as the start of a key descriptor.
    await user.click(within(dialog('domain')).getByLabelText('Pattern'));
    await user.paste('^[a-z.]+$');
    await user.type(within(dialog('domain')).getByLabelText('Max length'), '64');
    await done('domain');
    expect(schemaNow().fields[1]).toEqual({ name: 'domain', type: 'string', pattern: '^[a-z.]+$', max: 64 });
  });

  it('sets the item type of a list', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={{ fields: [{ name: 'ports', type: 'list', item: { name: '', type: 'string' } }] }} />);
    expect(within(dataRows()[0]).getAllByRole('cell')[1]).toHaveTextContent('list<string>');
    await openField('ports');
    await pickOption(within(dialog('ports')), 'Item type', { value: 'int' });
    await user.click(within(dialog('ports')).getByRole('button', { name: 'Done' }));
    expect(schemaNow().fields[0]).toEqual({ name: 'ports', type: 'list', item: { name: '', type: 'int' } });
    expect(within(dataRows()[0]).getAllByRole('cell')[1]).toHaveTextContent('list<int>');
  });

  it('moves and deletes fields', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={{ fields: [{ name: 'a', type: 'string' }, { name: 'b', type: 'string' }] }} />);
    await user.click(screen.getByRole('button', { name: 'Move a down' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b', 'a']);
    await openField('a');
    await user.click(within(dialog('a')).getByRole('button', { name: 'Delete field' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b']);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('keeps focus on the moved row', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={{ fields: [{ name: 'a', type: 'string' }, { name: 'b', type: 'string' }, { name: 'c', type: 'string' }] }} />);
    await user.click(screen.getByRole('button', { name: 'Move a down' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b', 'a', 'c']);
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Move a down' }));
    await user.click(screen.getByRole('button', { name: 'Move a down' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b', 'c', 'a']);
    // At the bottom "down" is disabled, so focus lands on "up" of the same row.
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Move a up' }));
  });

  it('marks fields with server problems and shows the message at the attribute', async () => {
    const user = userEvent.setup();
    render(
      <EditorHarness
        initial={{ fields: [{ name: 'vhosts', type: 'repeater', fields: [{ name: 'Bad', type: 'string' }] }, { name: 'ok', type: 'string' }] }}
        problems={[{ path: 'fields[0].fields[0].name', message: 'must start with a lowercase letter' }]}
      />,
    );
    expect(screen.getByRole('button', { name: /^vhosts.*has problems/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^ok/ })).not.toHaveAccessibleName(/has problems/);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(within(subField('Bad')).getByRole('alert')).toHaveTextContent('must start with a lowercase letter');
  });

  it('shows a top-level field problem in its dialog', async () => {
    render(
      <EditorHarness
        initial={{ fields: [{ name: 'Weird', type: 'string' }] }}
        problems={[
          { path: 'fields[0].name', message: 'must start with a lowercase letter' },
          { path: 'fields[0].type', message: 'unknown type weird' },
        ]}
      />,
    );
    await openField('Weird');
    expect(within(dialog('Weird')).getByText('must start with a lowercase letter')).toBeInTheDocument();
    expect(within(dialog('Weird')).getByText('unknown type weird')).toBeInTheDocument();
  });

  it('shows a top-level schema problem above the field list', () => {
    render(<EditorHarness initial={{ fields: [{ name: 'a', type: 'string' }] }} problems={[{ path: '', message: 'at most 100 fields are allowed' }]} />);
    expect(screen.getByText('at most 100 fields are allowed')).toBeInTheDocument();
  });

  it('marks the "has problems" dot with the destructive token, not a monitoring-state colour', () => {
    render(<EditorHarness initial={{ fields: [{ name: 'a', type: 'string' }] }} problems={[{ path: 'fields[0].name', message: 'bad' }]} />);
    const dot = screen.getByLabelText('has problems');
    expect(dot).toHaveClass('bg-destructive');
    expect(dot.getAttribute('style') ?? '').not.toContain('--px-crit');
  });
});
