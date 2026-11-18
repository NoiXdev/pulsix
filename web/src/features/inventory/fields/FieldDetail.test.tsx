import { describe, expect, it } from 'vitest';
import { useState } from 'react';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { FieldDetail } from './FieldDetail.tsx';
import type { SchemaField } from '../../variables/types.ts';

/** Harness holds one field the way the editor does and prints it. */
function Harness({ initial }: { initial: SchemaField }) {
  const [field, setField] = useState(initial);
  return (
    <>
      <FieldDetail field={field} path="fields[0]" onChange={setField} onRemove={() => undefined} />
      <pre data-testid="field">{JSON.stringify(field)}</pre>
    </>
  );
}

const now = (): SchemaField => JSON.parse(screen.getByTestId('field').textContent ?? '{}') as SchemaField;

describe('FieldDetail', () => {
  it('edits name and label, and offers every type as a card', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'env', type: 'string' }} />);
    await user.type(screen.getByLabelText('Name'), '_x');
    await user.type(screen.getByLabelText('Label'), 'Env');
    expect(now()).toEqual({ name: 'env_x', type: 'string', label: 'Env' });
    await user.clear(screen.getByLabelText('Label'));
    expect(now()).not.toHaveProperty('label');

    const types = within(screen.getByRole('group', { name: 'Type' })).getAllByRole('radio');
    expect(types).toHaveLength(9);
    expect(screen.getByRole('radio', { name: /^Text/ })).toBeChecked();
    await user.click(screen.getByRole('radio', { name: /^Choice/ }));
    expect(now()).toMatchObject({ type: 'enum', options: ['option_1'] });
  });

  it('sets the scope with the segmented control and required and active with switches', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'env', type: 'string', overridable: true }} />);
    await user.click(screen.getByRole('radio', { name: 'Per host' }));
    expect(now()).toEqual({ name: 'env', type: 'string', scope: 'host' });
    await user.click(screen.getByRole('switch', { name: 'Required' }));
    expect(now().required).toBe(true);
    await user.click(screen.getByRole('switch', { name: 'Active' }));
    expect(now().active).toBe(false);
    await user.click(screen.getByRole('switch', { name: 'Active' }));
    expect(now()).not.toHaveProperty('active');
  });

  it('keeps "hosts may override" and the other settings reachable', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'env', type: 'string' }} />);
    const more = screen.getByRole('region', { name: 'More settings' });
    await user.type(within(more).getByLabelText('Description'), 'Where it runs');
    await user.click(within(more).getByRole('switch', { name: 'Hosts may override' }));
    await user.type(within(more).getByLabelText('Max length'), '8');
    await user.type(within(more).getByLabelText('Pattern'), '^[[a-z]+$');
    expect(now()).toEqual({ name: 'env', type: 'string', description: 'Where it runs', overridable: true, max: 8, pattern: '^[a-z]+$' });
  });

  it('takes a text default', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'env', type: 'string' }} />);
    await user.type(screen.getByLabelText('Default'), 'prod');
    expect(now().default).toBe('prod');
  });

  it('takes a number default for an integer', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'port', type: 'int' }} />);
    await user.type(screen.getByLabelText('Default'), '8080');
    expect(now().default).toBe(8080);
  });

  it('takes a yes/no default for a bool, and none', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'debug', type: 'bool' }} />);
    const group = screen.getByRole('group', { name: 'Default' });
    expect(within(group).getByRole('radio', { name: 'None' })).toBeChecked();
    await user.click(within(group).getByRole('radio', { name: 'Yes' }));
    expect(now().default).toBe(true);
    await user.click(within(group).getByRole('radio', { name: 'No' }));
    expect(now().default).toBe(false);
    await user.click(within(group).getByRole('radio', { name: 'None' }));
    expect(now()).not.toHaveProperty('default');
  });

  it('edits enum options as chips: click makes the default, ✕ removes, + Option adds', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'tier', type: 'enum', options: ['gold', 'silver'], default: 'gold' }} />);
    expect(screen.getByRole('button', { name: 'gold' })).toHaveAttribute('aria-pressed', 'true');
    await user.click(screen.getByRole('button', { name: 'silver' }));
    expect(now().default).toBe('silver');
    await user.click(screen.getByRole('button', { name: 'silver' }));
    expect(now()).not.toHaveProperty('default');

    await user.click(screen.getByRole('button', { name: 'Option' }));
    await user.type(screen.getByRole('textbox', { name: 'New option' }), 'bronze{Enter}copper{Enter}');
    expect(now().options).toEqual(['gold', 'silver', 'bronze', 'copper']);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('textbox', { name: 'New option' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'gold' }));
    await user.click(screen.getByRole('button', { name: 'Remove option gold' }));
    expect(now().options).toEqual(['silver', 'bronze', 'copper']);
    expect(now()).not.toHaveProperty('default');
  });

  it('says a secret has no default instead of offering one', () => {
    render(<Harness initial={{ name: 'token', type: 'secret' }} />);
    expect(screen.getByText(/A secret has no default/)).toBeInTheDocument();
    expect(screen.queryByLabelText('Default')).not.toBeInTheDocument();
  });

  it('takes a list default as comma separated text of its item type', async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'ports', type: 'list', item: { name: '', type: 'int' } }} />);
    await user.type(screen.getByLabelText('Default'), '80, 443');
    expect(now().default).toEqual([80, 443]);
    expect(screen.getByLabelText('Default')).toHaveValue('80, 443');
    expect(screen.getByText('Comma separated')).toBeInTheDocument();
  });

  it("edits a repeater's sub-fields in its panel", async () => {
    const user = userEvent.setup();
    render(<Harness initial={{ name: 'vhosts', type: 'repeater', fields: [{ name: 'domain', type: 'string' }] }} />);
    expect(screen.getByText('This type has no default.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Add sub-field' }));
    expect(now().fields?.map((f) => f.name)).toEqual(['domain', 'field_2']);
  });
});
