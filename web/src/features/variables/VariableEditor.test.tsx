import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { pickOption } from '../../test/pickOption.ts';
import { VariableEditor } from './VariableEditor.tsx';
import type { EditorVariant, FieldSource, Problem, VarSchema } from './types.ts';

function Harness({ initial = { fields: [] }, problems, variant = 'group' }: { initial?: VarSchema; problems?: Problem[]; variant?: EditorVariant }) {
  const [value, setValue] = useState<VarSchema>(initial);
  return (
    <>
      <VariableEditor value={value} onChange={setValue} problems={problems} variant={variant} />
      <pre data-testid="schema">{JSON.stringify(value)}</pre>
    </>
  );
}

const schemaNow = () => JSON.parse(screen.getByTestId('schema').textContent ?? '{}') as VarSchema;
const dialog = (name: string) => screen.getByRole('dialog', { name: `Field ${name}` });
const subField = (name: string) => screen.getByRole('group', { name: `Sub-field ${name}` });
const dataRows = () => within(screen.getByRole('table', { name: 'Fields' })).getAllByRole('row').slice(1);
const openField = (name: string) => userEvent.setup().click(screen.getByRole('button', { name: new RegExp(`^${name}\\b`) }));
const done = (name: string) => userEvent.setup().click(within(dialog(name)).getByRole('button', { name: 'Done' }));

const src: FieldSource = { kind: 'argument_spec', role: 'web', file: 'roles/web/meta/argument_specs.yml', line: 3 };

describe('VariableEditor — the field list', () => {
  it('shows one row per top-level field with the six columns', () => {
    render(
      <Harness
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

  it('has no origin column outside job templates', () => {
    render(<Harness variant="group" initial={{ fields: [{ name: 'env', type: 'string' }] }} />);
    expect(screen.queryByRole('columnheader', { name: 'Origin' })).not.toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Active' })).toBeInTheDocument();
  });

  it('switches a field inactive and active again', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ fields: [{ name: 'env', type: 'string' }] }} />);
    await user.click(screen.getByRole('switch', { name: 'env active' }));
    expect(schemaNow().fields[0]).toEqual({ name: 'env', type: 'string', active: false });
    await user.click(screen.getByRole('switch', { name: 'env active' }));
    expect(schemaNow().fields[0]).toEqual({ name: 'env', type: 'string' });
  });

  it('keeps a source field locked when it is only switched inactive', async () => {
    const user = userEvent.setup();
    render(<Harness variant="template" initial={{ fields: [{ name: 'port', type: 'int', origin: 'source', source: src }] }} />);
    await user.click(screen.getByRole('switch', { name: 'port active' }));
    expect(schemaNow().fields[0]).toEqual({ name: 'port', type: 'int', origin: 'source', source: src, active: false });
  });

  it('shows the four origins on a job template', () => {
    render(
      <Harness
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
    render(<Harness variant="template" initial={{ fields: [{ name: 'port', type: 'int', origin: 'source', source: src }] }} />);
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
    render(<Harness />);
    await user.click(screen.getByRole('button', { name: 'Add field' }));
    await user.click(within(dialog('field_1')).getByRole('button', { name: 'Cancel' }));
    expect(schemaNow().fields).toEqual([]);
  });

  it('makes a source field adjusted when its dialog changes it', async () => {
    const user = userEvent.setup();
    render(<Harness variant="template" initial={{ fields: [{ name: 'port', type: 'int', default: 80, origin: 'source', source: src }] }} />);
    await openField('port');
    await user.type(within(dialog('port')).getByLabelText('Label'), 'HTTP port');
    await done('port');
    expect(schemaNow().fields[0]).toEqual({
      name: 'port', type: 'int', default: 80, label: 'HTTP port', origin: 'adjusted', source: src,
      source_field: { name: 'port', type: 'int', default: 80 },
    });
  });
});

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
    render(<Harness initial={vhosts()} />);
    expect(screen.queryByRole('group', { name: 'Sub-field domain' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    await user.click(within(subField('domain')).getByLabelText('Required'));
    await user.click(screen.getByRole('button', { name: 'Add sub-field' }));
    await pickOption(within(subField('field_2')), 'Type', { value: 'secret' });
    expect(schemaNow().fields[0].fields).toEqual([{ name: 'domain', type: 'string', required: true }, { name: 'field_2', type: 'secret' }]);
  });

  it('makes a source field adjusted when one of its sub-fields is edited', async () => {
    const user = userEvent.setup();
    render(<Harness variant="template" initial={vhosts('source')} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    await user.click(within(subField('domain')).getByLabelText('Required'));
    const f = schemaNow().fields[0];
    expect(f.origin).toBe('adjusted');
    expect(f.source_field).toEqual({ name: 'vhosts', type: 'repeater', fields: [{ name: 'domain', type: 'string' }] });
    expect(f.fields).toEqual([{ name: 'domain', type: 'string', required: true }]);
  });

  it('offers no new sub-field on a source field until it is adjusted', async () => {
    const user = userEvent.setup();
    const { unmount } = render(<Harness variant="template" initial={vhosts('source')} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(screen.getByRole('button', { name: 'Add sub-field' })).toBeDisabled();
    unmount();
    render(<Harness variant="template" initial={vhosts('adjusted')} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(screen.getByRole('button', { name: 'Add sub-field' })).toBeEnabled();
  });

  it('edits the attributes of a sub-field in its own dialog', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ fields: [{ name: 'auth', type: 'object', fields: [{ name: 'mode', type: 'enum', options: ['a'] }] }] }} />);
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
      <Harness
        initial={vhosts()}
        problems={[{ path: 'fields[0].fields', message: 'schemas nest at most 4 levels deep' }]}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Sub-fields of vhosts' }));
    expect(screen.getByText('schemas nest at most 4 levels deep')).toBeInTheDocument();
  });
});

describe('VariableEditor — variants', () => {
  it('asks for scope on a group and previews the assignment form', async () => {
    const user = userEvent.setup();
    render(
      <Harness
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
    render(<Harness variant="template" initial={{ fields: [{ name: 'env', type: 'string' }] }} />);
    expect(screen.queryByRole('region', { name: 'Assignment form preview' })).not.toBeInTheDocument();
    await openField('env');
    expect(within(dialog('env')).queryByText('Scope')).not.toBeInTheDocument();
  });

  it('offers no secret type on a shared definition', async () => {
    const user = userEvent.setup();
    render(<Harness variant="shared" initial={{ fields: [{ name: 'region', type: 'string' }] }} />);
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
});

describe('VariableEditor — migrated builder behaviour', () => {
  it('edits name, type and required of a field', async () => {
    const user = userEvent.setup();
    render(<Harness />);
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
    render(<Harness initial={{ fields: [{ name: 'php', type: 'enum', options: ['8.3'] }, { name: 'domain', type: 'string' }] }} />);
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
    render(<Harness initial={{ fields: [{ name: 'ports', type: 'list', item: { name: '', type: 'string' } }] }} />);
    expect(within(dataRows()[0]).getAllByRole('cell')[1]).toHaveTextContent('list<string>');
    await openField('ports');
    await pickOption(within(dialog('ports')), 'Item type', { value: 'int' });
    await user.click(within(dialog('ports')).getByRole('button', { name: 'Done' }));
    expect(schemaNow().fields[0]).toEqual({ name: 'ports', type: 'list', item: { name: '', type: 'int' } });
    expect(within(dataRows()[0]).getAllByRole('cell')[1]).toHaveTextContent('list<int>');
  });

  it('moves and deletes fields', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ fields: [{ name: 'a', type: 'string' }, { name: 'b', type: 'string' }] }} />);
    await user.click(screen.getByRole('button', { name: 'Move a down' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b', 'a']);
    await openField('a');
    await user.click(within(dialog('a')).getByRole('button', { name: 'Delete field' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b']);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('keeps focus on the moved row', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ fields: [{ name: 'a', type: 'string' }, { name: 'b', type: 'string' }, { name: 'c', type: 'string' }] }} />);
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
      <Harness
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
      <Harness
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
    render(<Harness initial={{ fields: [{ name: 'a', type: 'string' }] }} problems={[{ path: '', message: 'at most 100 fields are allowed' }]} />);
    expect(screen.getByText('at most 100 fields are allowed')).toBeInTheDocument();
  });

  it('marks the "has problems" dot with the destructive token, not a monitoring-state colour', () => {
    render(<Harness initial={{ fields: [{ name: 'a', type: 'string' }] }} problems={[{ path: 'fields[0].name', message: 'bad' }]} />);
    const dot = screen.getByLabelText('has problems');
    expect(dot).toHaveClass('bg-destructive');
    expect(dot.getAttribute('style') ?? '').not.toContain('--px-crit');
  });
});
