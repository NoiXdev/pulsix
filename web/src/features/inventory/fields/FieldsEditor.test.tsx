import { describe, expect, it } from 'vitest';
import { useState } from 'react';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { FieldsEditor } from './FieldsEditor.tsx';
import type { Problem, VarSchema } from '../../variables/types.ts';

/** Harness holds the editor's schema the way the dialog does and prints
 * it, so a test reads what an interaction produced. */
function Harness({ initial, problems, readOnly }: { initial: VarSchema; problems?: Problem[]; readOnly?: boolean }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <FieldsEditor schema={value} onChange={setValue} problems={problems} readOnly={readOnly} />
      <pre data-testid="schema">{JSON.stringify(value)}</pre>
    </>
  );
}

const schemaNow = (): VarSchema => JSON.parse(screen.getByTestId('schema').textContent ?? '{}') as VarSchema;
const rows = () => within(screen.getByRole('list', { name: 'Fields' })).getAllByRole('listitem');
const row = (name: string) => rows().find((r) => within(r).queryByRole('button', { name: new RegExp(`^${name}\\b`) })) as HTMLElement;
const select = (user: ReturnType<typeof userEvent.setup>, name: string) => user.click(within(row(name)).getByRole('button', { name: new RegExp(`^${name}\\b`) }));
const panel = () => screen.getByRole('tabpanel');

const BASE: VarSchema = {
  fields: [
    { name: 'env', label: 'Environment', type: 'string', required: true, default: 'prod' },
    { name: 'domain', type: 'string', scope: 'host' },
    { name: 'tier', type: 'enum', options: ['gold', 'silver'], default: 'gold' },
  ],
};

describe('FieldsEditor — the rows', () => {
  it('shows one readable row per field: name, label, default and three badges', () => {
    render(<Harness initial={BASE} />);
    expect(rows()).toHaveLength(3);
    const env = row('env');
    expect(env).toHaveTextContent('Environment · default prod');
    expect(within(env).getByRole('button', { name: 'Type of env: Text' })).toHaveAttribute('aria-haspopup', 'menu');
    expect(within(env).getByRole('button', { name: 'Scope of env: Group' })).toBeInTheDocument();
    expect(within(env).getByRole('button', { name: 'Requirement of env: Required' })).toBeInTheDocument();
    expect(within(row('domain')).getByRole('button', { name: 'Scope of domain: Per host' })).toBeInTheDocument();
    expect(within(row('domain')).getByRole('button', { name: 'Requirement of domain: Optional' })).toBeInTheDocument();
    expect(row('domain')).toHaveTextContent('no default');
  });

  it('toggles the scope and required with one click each', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    await user.click(screen.getByRole('button', { name: 'Scope of env: Group' }));
    expect(schemaNow().fields[0].scope).toBe('host');
    await user.click(screen.getByRole('button', { name: 'Scope of env: Per host' }));
    expect(schemaNow().fields[0]).not.toHaveProperty('scope');
    await user.click(screen.getByRole('button', { name: 'Requirement of env: Required' }));
    expect(schemaNow().fields[0]).not.toHaveProperty('required');
    await user.click(screen.getByRole('button', { name: 'Requirement of env: Optional' }));
    expect(schemaNow().fields[0].required).toBe(true);
  });

  it('sets the type from the badge menu, which lists every type with its help', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    const badge = screen.getByRole('button', { name: 'Type of env: Text' });
    await user.click(badge);
    const menu = screen.getByRole('menu', { name: 'Type of env' });
    const items = within(menu).getAllByRole('menuitemradio');
    expect(items.map((i) => i.querySelector('span span')?.textContent)).toEqual(['Text', 'Integer', 'Decimal', 'Yes/No', 'Choice', 'Secret', 'List', 'Repeater', 'Object']);
    expect(within(menu).getByRole('menuitemradio', { name: /^Text/ })).toHaveAttribute('aria-checked', 'true');
    expect(within(menu).getByRole('menuitemradio', { name: /^Integer/ })).toHaveTextContent('A whole number');
    expect(within(menu).getByRole('menuitemradio', { name: /^Text/ })).toHaveFocus();

    await user.keyboard('{ArrowDown}{Enter}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(schemaNow().fields[0]).toMatchObject({ name: 'env', type: 'int', required: true });
    expect(schemaNow().fields[0]).not.toHaveProperty('default');
    expect(screen.getByRole('button', { name: 'Type of env: Integer' })).toHaveFocus();
  });

  it('closes the type menu on Escape without a change', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    await user.click(screen.getByRole('button', { name: 'Type of env: Text' }));
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(schemaNow()).toEqual(BASE);
  });

  it('selects the first field, and another by its row button', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    expect(within(row('env')).getByRole('button', { name: /^env\b/ })).toHaveAttribute('aria-pressed', 'true');
    expect(within(panel()).getByLabelText('Name')).toHaveValue('env');
    await select(user, 'tier');
    expect(within(row('tier')).getByRole('button', { name: /^tier\b/ })).toHaveAttribute('aria-pressed', 'true');
    expect(within(row('env')).getByRole('button', { name: /^env\b/ })).toHaveAttribute('aria-pressed', 'false');
    expect(within(panel()).getByLabelText('Name')).toHaveValue('tier');
  });

  it('selects a row from the keyboard', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    within(row('domain')).getByRole('button', { name: /^domain\b/ }).focus();
    await user.keyboard('{Enter}');
    expect(within(panel()).getByLabelText('Name')).toHaveValue('domain');
  });

  it('adds a field, selects it and focuses its name', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    await user.click(screen.getByRole('button', { name: 'Add field' }));
    expect(rows()).toHaveLength(4);
    expect(schemaNow().fields[3]).toEqual({ name: 'field_4', type: 'string' });
    expect(within(row('field_4')).getByRole('button', { name: /^field_4\b/ })).toHaveAttribute('aria-pressed', 'true');
    expect(within(panel()).getByLabelText('Name')).toHaveFocus();
    await user.clear(within(panel()).getByLabelText('Name'));
    await user.type(within(panel()).getByLabelText('Name'), 'region');
    expect(schemaNow().fields[3].name).toBe('region');
  });

  it('removes the selected field and selects the next one', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    await select(user, 'domain');
    await user.click(within(panel()).getByRole('button', { name: 'Remove field' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['env', 'tier']);
    expect(within(panel()).getByLabelText('Name')).toHaveValue('tier');
  });

  it('reorders with up and down, keeping the focus on the moved row', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    await user.click(screen.getByRole('button', { name: 'Move env down' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['domain', 'env', 'tier']);
    expect(screen.getByRole('button', { name: 'Move env down' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Move tier up' }));
    expect(schemaNow().fields.map((f) => f.name)).toEqual(['domain', 'tier', 'env']);
    expect(screen.getByRole('button', { name: 'Move domain up' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Move env down' })).toBeDisabled();
  });

  it('marks a field with problems and shows them at its attributes', async () => {
    render(<Harness initial={BASE} problems={[{ path: 'fields[0].name', message: 'is taken' }, { path: 'fields[0].min', message: 'too small' }]} />);
    expect(within(row('env')).getByRole('img', { name: 'has problems' })).toBeInTheDocument();
    expect(within(row('tier')).queryByRole('img', { name: 'has problems' })).not.toBeInTheDocument();
    expect(within(panel()).getByText('is taken')).toBeInTheDocument();
    expect(within(panel()).getByText('too small')).toBeInTheDocument();
  });

  it('shows the assignment form preview in its own tab', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} />);
    await user.click(screen.getByRole('tab', { name: 'Assign preview' }));
    expect(within(panel()).getByRole('region', { name: 'Assignment form preview' })).toBeInTheDocument();
    // The host field is what an operator fills in on assignment.
    expect(within(panel()).getByLabelText('domain')).toBeInTheDocument();
    await select(user, 'env');
    expect(screen.getByRole('tab', { name: 'Field' })).toHaveAttribute('aria-selected', 'true');
  });
});

describe('FieldsEditor — a reader', () => {
  it('shows the rows and the selected field read-only', async () => {
    const user = userEvent.setup();
    render(<Harness initial={BASE} readOnly />);
    expect(screen.queryByRole('button', { name: 'Add field' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Type of env/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Scope of/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Requirement of/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Move / })).not.toBeInTheDocument();
    expect(within(row('env')).getByRole('img', { name: 'Type of env: Text' })).toBeInTheDocument();
    expect(within(row('env')).getByRole('img', { name: 'Requirement of env: Required' })).toBeInTheDocument();

    await select(user, 'tier');
    expect(within(panel()).getByLabelText('Name')).toBeDisabled();
    expect(within(panel()).getByRole('radio', { name: /^Choice/ })).toBeDisabled();
    expect(within(panel()).getByRole('switch', { name: 'Active' })).toBeDisabled();
    expect(within(panel()).queryByRole('button', { name: 'Remove field' })).not.toBeInTheDocument();
    expect(within(panel()).getByRole('button', { name: 'Remove option gold' })).toBeDisabled();
  });
});
