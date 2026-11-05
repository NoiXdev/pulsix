import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { pickOption } from '../../test/pickOption.ts';
import { SchemaBuilder } from './SchemaBuilder.tsx';
import type { Problem, VarSchema } from './types.ts';

function Harness({ initial = { fields: [] }, problems }: { initial?: VarSchema; problems?: Problem[] }) {
  const [value, setValue] = useState<VarSchema>(initial);
  return (
    <>
      <SchemaBuilder value={value} onChange={setValue} problems={problems} />
      <pre data-testid="schema">{JSON.stringify(value)}</pre>
    </>
  );
}

const schemaNow = () => JSON.parse(screen.getByTestId('schema').textContent ?? '{}') as VarSchema;
const editor = (name: string) => screen.getByRole('group', { name: `Field ${name}` });

describe('SchemaBuilder', () => {
  it('adds a field and edits name, type, scope and required', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole('button', { name: 'Add field' }));
    const ed = editor('field_1');
    const name = within(ed).getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'vhosts');
    await pickOption(within(editor('vhosts')), 'Type', { value: 'repeater' });
    await user.click(within(editor('vhosts')).getByLabelText(/Host \(asked/));
    // The sub-schema's own editor sits inside this one, so take the first "Required".
    await user.click(within(editor('vhosts')).getAllByLabelText('Required')[0]);
    expect(schemaNow()).toEqual({
      fields: [{ name: 'vhosts', type: 'repeater', scope: 'host', required: true, fields: [{ name: 'value', type: 'string' }] }],
    });
    expect(within(editor('vhosts')).queryByLabelText('Hosts may override')).not.toBeInTheDocument();
  });

  it('edits a sub-schema with the nested builder', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ fields: [{ name: 'vhosts', type: 'repeater', scope: 'host', fields: [{ name: 'value', type: 'string' }] }] }} />);
    const outer = editor('vhosts');
    await user.click(within(outer).getByRole('button', { name: 'Add sub-field' }));
    const inner = editor('field_2');
    await pickOption(within(inner), 'Type', { value: 'secret' });
    expect(within(inner).queryByLabelText(/Host \(asked/)).not.toBeInTheDocument();
    expect(schemaNow().fields[0].fields).toEqual([{ name: 'value', type: 'string' }, { name: 'field_2', type: 'secret' }]);
  });

  it('edits enum options, bounds, pattern and default', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ fields: [{ name: 'php', type: 'enum', options: ['8.3'] }, { name: 'domain', type: 'string' }] }} />);
    const options = within(editor('php')).getByLabelText('Options (one per line)');
    await user.clear(options);
    await user.type(options, '8.3{enter}8.4');
    await pickOption(within(editor('php')), 'Default', { value: '8.4' });
    expect(schemaNow().fields[0]).toEqual({ name: 'php', type: 'enum', options: ['8.3', '8.4'], default: '8.4' });

    await user.click(screen.getByRole('button', { name: /^domain/ }));
    // paste, not type: user-event reads "[" as the start of a key descriptor.
    await user.click(within(editor('domain')).getByLabelText('Pattern'));
    await user.paste('^[a-z.]+$');
    await user.type(within(editor('domain')).getByLabelText('Max length'), '64');
    expect(schemaNow().fields[1]).toEqual({ name: 'domain', type: 'string', pattern: '^[a-z.]+$', max: 64 });
  });

  it('moves and deletes fields', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ fields: [{ name: 'a', type: 'string' }, { name: 'b', type: 'string' }] }} />);
    await user.click(within(editor('a')).getByRole('button', { name: 'Move down' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b', 'a']);
    await user.click(within(editor('a')).getByRole('button', { name: 'Delete field' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b']);
  });

  it('keeps the field editor and its focus when the selected field moves', async () => {
    const user = userEvent.setup();
    render(
      <Harness
        initial={{
          fields: [{ name: 'a', type: 'string' }, { name: 'b', type: 'string' }, { name: 'c', type: 'string' }],
        }}
      />,
    );
    const moveDown = within(editor('a')).getByRole('button', { name: 'Move down' });
    moveDown.focus();
    await user.click(moveDown);
    // Same button instance, still focused: the editor did not remount.
    expect(document.activeElement).toBe(moveDown);
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b', 'a', 'c']);

    await user.click(moveDown);
    expect(document.activeElement).toBe(moveDown);
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['b', 'c', 'a']);
  });

  it('marks fields with server problems and shows the message at the attribute', () => {
    render(
      <Harness
        initial={{ fields: [{ name: 'vhosts', type: 'repeater', fields: [{ name: 'Bad', type: 'string' }] }] }}
        problems={[{ path: 'fields[0].fields[0].name', message: 'must start with a lowercase letter' }]}
      />,
    );
    expect(screen.getByRole('button', { name: /^vhosts.*has problems/ })).toBeInTheDocument();
    expect(within(editor('Bad')).getByRole('alert')).toHaveTextContent('must start with a lowercase letter');
  });

  it('gives each selected field its own editor state instead of reusing the previous one', async () => {
    const user = userEvent.setup();
    render(
      <Harness
        initial={{
          fields: [
            { name: 'php', type: 'enum', options: ['8.3', '8.4'] },
            { name: 'db', type: 'enum', options: ['mysql', 'postgres'] },
          ],
        }}
      />,
    );
    // php is selected first (index 0); switch to db, which sits at the same
    // index-independent slot in the editor.
    await user.click(screen.getByRole('button', { name: /^db/ }));
    const dbOptions = within(editor('db')).getByLabelText('Options (one per line)');
    expect(dbOptions).toHaveValue('mysql\npostgres');

    await user.clear(dbOptions);
    await user.type(dbOptions, 'mysql{enter}mariadb');

    expect(schemaNow().fields[0]).toEqual({ name: 'php', type: 'enum', options: ['8.3', '8.4'] });
    expect(schemaNow().fields[1]).toEqual({ name: 'db', type: 'enum', options: ['mysql', 'mariadb'] });
  });

  it('shows a repeater/object .fields problem in its Sub-schema section', () => {
    render(
      <Harness
        initial={{ fields: [{ name: 'vhosts', type: 'repeater', fields: [{ name: 'value', type: 'string' }] }] }}
        problems={[{ path: 'fields[0].fields', message: 'schemas nest at most 4 levels deep' }]}
      />,
    );
    expect(within(editor('vhosts')).getByText('schemas nest at most 4 levels deep')).toBeInTheDocument();
  });

  it('shows a field .type problem next to the Type select', () => {
    render(
      <Harness
        initial={{ fields: [{ name: 'weird', type: 'string' }] }}
        problems={[{ path: 'fields[0].type', message: 'unknown type weird' }]}
      />,
    );
    expect(within(editor('weird')).getByText('unknown type weird')).toBeInTheDocument();
  });

  it('shows a top-level schema problem above the field list', () => {
    render(
      <Harness
        initial={{ fields: [{ name: 'a', type: 'string' }] }}
        problems={[{ path: '', message: 'at most 100 fields are allowed' }]}
      />,
    );
    expect(screen.getByText('at most 100 fields are allowed')).toBeInTheDocument();
  });

  it('marks the "has problems" dot with the destructive token, not a monitoring-state colour', () => {
    render(
      <Harness
        initial={{ fields: [{ name: 'a', type: 'string' }] }}
        problems={[{ path: 'fields[0].name', message: 'bad' }]}
      />,
    );
    const dot = screen.getByLabelText('has problems');
    expect(dot).toHaveClass('bg-destructive');
    expect(dot.getAttribute('style') ?? '').not.toContain('--px-crit');
  });

  it('previews the assignment form from host fields and overrides', () => {
    render(
      <Harness
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
  });
});
