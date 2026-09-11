import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SchemaForm } from './SchemaForm.tsx';
import { keepMarker } from './secrets.ts';
import type { Problem, SchemaField, Vars } from './types.ts';

function Harness({ fields, initial = {}, problems, disabled }: { fields: SchemaField[]; initial?: Vars; problems?: Problem[]; disabled?: boolean }) {
  const [value, setValue] = useState<Vars>(initial);
  return (
    <>
      <SchemaForm fields={fields} value={value} onChange={setValue} problems={problems} disabled={disabled} />
      <pre data-testid="value">{JSON.stringify(value)}</pre>
    </>
  );
}

const current = () => JSON.parse(screen.getByTestId('value').textContent ?? '{}') as Vars;

const vhosts: SchemaField = {
  name: 'vhosts', label: 'Virtual hosts', type: 'repeater', scope: 'host', required: true,
  fields: [
    { name: 'domain', type: 'string', required: true },
    { name: 'ssl', type: 'bool', default: true },
    { name: 'password', type: 'secret' },
  ],
};

describe('SchemaForm', () => {
  it('edits scalar fields and clears a key when the input is emptied', async () => {
    const user = userEvent.setup();
    render(
      <Harness
        fields={[
          { name: 'env', type: 'string', required: true, description: 'Deployment stage' },
          { name: 'workers', type: 'int', default: 4 },
          { name: 'ratio', type: 'float' },
          { name: 'php', type: 'enum', options: ['8.3', '8.4'] },
          { name: 'debug', type: 'bool' },
        ]}
      />,
    );
    expect(screen.getByText('Deployment stage')).toBeInTheDocument();
    await user.type(screen.getByLabelText('env'), 'prod');
    await user.type(screen.getByLabelText('workers'), '8');
    await user.type(screen.getByLabelText('ratio'), '0.5');
    await user.selectOptions(screen.getByLabelText('php'), '8.4');
    await user.click(screen.getByLabelText('debug'));
    expect(current()).toEqual({ env: 'prod', workers: 8, ratio: 0.5, php: '8.4', debug: true });
    await user.clear(screen.getByLabelText('env'));
    expect(current()).not.toHaveProperty('env');
    expect(screen.getByLabelText('workers')).toHaveAttribute('placeholder', '4');
  });

  it('adds, edits, reorders and removes repeater entries', async () => {
    const user = userEvent.setup();
    render(<Harness fields={[vhosts]} />);
    await user.click(screen.getByRole('button', { name: 'Add Virtual hosts' }));
    await user.type(screen.getByLabelText('domain'), 'a.io');
    await user.click(screen.getByRole('button', { name: 'Add Virtual hosts' }));
    await user.type(screen.getAllByLabelText('domain')[1], 'b.io');
    expect(current()).toEqual({ vhosts: [{ domain: 'a.io' }, { domain: 'b.io' }] });

    await user.click(screen.getByRole('button', { name: 'Move Virtual hosts 2 up' }));
    expect(current()).toEqual({ vhosts: [{ domain: 'b.io' }, { domain: 'a.io' }] });

    await user.click(screen.getByRole('button', { name: 'Remove Virtual hosts 1' }));
    expect(current()).toEqual({ vhosts: [{ domain: 'a.io' }] });
  });

  it('shows a stored secret as stored, replaces it, and can keep it again', async () => {
    const user = userEvent.setup();
    render(<Harness fields={[{ name: 'db_password', type: 'secret' }]} initial={{ db_password: keepMarker('db_password') }} />);
    const stored = screen.getByLabelText('db_password');
    expect(stored).toHaveValue('•••••••• stored');
    expect(stored).toHaveAttribute('readonly');
    await user.click(screen.getByRole('button', { name: 'Replace' }));
    await user.type(screen.getByLabelText('db_password'), 'n3w');
    expect(current()).toEqual({ db_password: 'n3w' });
    await user.click(screen.getByRole('button', { name: 'Keep stored value' }));
    expect(current()).toEqual({ db_password: keepMarker('db_password') });
  });

  it('keeps a moved entry’s stored secret with that entry', async () => {
    const user = userEvent.setup();
    render(
      <Harness
        fields={[vhosts]}
        initial={{ vhosts: [{ domain: 'a.io', password: keepMarker('vhosts[0].password') }, { domain: 'b.io' }] }}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Move Virtual hosts 1 down' }));
    const second = screen.getByRole('group', { name: 'Virtual hosts 2' });
    await user.click(within(second).getByRole('button', { name: 'Replace' }));
    await user.click(within(second).getByRole('button', { name: 'Keep stored value' }));
    expect(current()).toEqual({ vhosts: [{ domain: 'b.io' }, { domain: 'a.io', password: keepMarker('vhosts[0].password') }] });
  });

  it('edits list items and nested objects', async () => {
    const user = userEvent.setup();
    render(
      <Harness
        fields={[
          { name: 'aliases', type: 'list', item: { name: '', type: 'string' } },
          { name: 'auth', type: 'object', fields: [{ name: 'user', type: 'string' }] },
        ]}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Add aliases' }));
    await user.type(screen.getByLabelText('aliases 1'), 'www.a.io');
    await user.type(screen.getByLabelText('user'), 'admin');
    expect(current()).toEqual({ aliases: ['www.a.io'], auth: { user: 'admin' } });
    await user.click(screen.getByRole('button', { name: 'Remove aliases 1' }));
    await user.clear(screen.getByLabelText('user'));
    expect(current()).toEqual({});
  });

  it('shows each server problem at its field', () => {
    render(
      <Harness
        fields={[vhosts]}
        initial={{ vhosts: [{ domain: 'x' }, {}] }}
        problems={[{ path: 'vhosts[1].domain', message: 'is required' }, { path: 'vhosts', message: 'needs more' }]}
      />,
    );
    const second = screen.getByRole('group', { name: 'Virtual hosts 2' });
    expect(within(second).getByRole('alert')).toHaveTextContent('is required');
    expect(within(second).getByLabelText('domain')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByText('needs more')).toBeInTheDocument();
  });

  it('renders read-only when disabled', () => {
    render(<Harness fields={[{ name: 'env', type: 'string' }, vhosts]} initial={{ env: 'prod' }} disabled />);
    expect(screen.getByLabelText('env')).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Add Virtual hosts' })).toBeDisabled();
  });

  it('says so when a schema has no fields', () => {
    render(<Harness fields={[]} />);
    expect(screen.getByText('No fields.')).toBeInTheDocument();
  });
});
