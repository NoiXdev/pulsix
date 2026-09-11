import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
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
    await user.selectOptions(within(editor('vhosts')).getByLabelText('Type'), 'repeater');
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
    await user.selectOptions(within(inner).getByLabelText('Type'), 'secret');
    expect(within(inner).queryByLabelText(/Host \(asked/)).not.toBeInTheDocument();
    expect(schemaNow().fields[0].fields).toEqual([{ name: 'value', type: 'string' }, { name: 'field_2', type: 'secret' }]);
  });

  it('edits enum options, bounds, pattern and default', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ fields: [{ name: 'php', type: 'enum', options: ['8.3'] }, { name: 'domain', type: 'string' }] }} />);
    const options = within(editor('php')).getByLabelText('Options (one per line)');
    await user.clear(options);
    await user.type(options, '8.3{enter}8.4');
    await user.selectOptions(within(editor('php')).getByLabelText('Default'), '8.4');
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
