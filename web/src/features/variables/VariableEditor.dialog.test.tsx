import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { FieldUsage } from './usage.ts';
import type { VarSchema } from './types.ts';
import { EditorHarness } from './EditorHarness.tsx';
import { dialog, subField } from './editorTestKit.ts';

describe('VariableEditor — focus after a dialog', () => {
  const fields: VarSchema = { fields: [{ name: 'a', type: 'string' }, { name: 'b', type: 'string' }] };
  const rowButton = (name: string) => screen.getByRole('button', { name: new RegExp(`^${name}\\b`) });

  it('returns to the field that was opened after Done', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={fields} />);
    await user.click(rowButton('b'));
    await user.click(within(dialog('b')).getByRole('button', { name: 'Done' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(document.activeElement).toBe(rowButton('b'));
  });

  it('returns to the field that was opened after Cancel', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={fields} />);
    await user.click(rowButton('a'));
    await user.click(within(dialog('a')).getByRole('button', { name: 'Cancel' }));
    expect(document.activeElement).toBe(rowButton('a'));
  });

  it('falls back to "Add field" when the field it was opened from is gone', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={fields} />);
    await user.click(rowButton('a'));
    await user.click(within(dialog('a')).getByRole('button', { name: 'Delete field' }));
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add field' }));
  });

  it('returns to "Add field" after a new field is added', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={fields} />);
    await user.click(screen.getByRole('button', { name: 'Add field' }));
    await user.click(within(dialog('field_3')).getByRole('button', { name: 'Done' }));
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add field' }));
  });

  it('returns to the sub-field that was opened after Done and Cancel, and to "Add sub-field" after a delete', async () => {
    const user = userEvent.setup();
    render(<EditorHarness initial={{ fields: [{ name: 'auth', type: 'object', fields: [{ name: 'mode', type: 'string' }, { name: 'user', type: 'string' }] }] }} />);
    await user.click(screen.getByRole('button', { name: 'Sub-fields of auth' }));
    const edit = (name: string) => within(subField(name)).getByRole('button', { name: `Edit ${name}` });
    await user.click(edit('user'));
    await user.click(within(dialog('user')).getByRole('button', { name: 'Done' }));
    expect(document.activeElement).toBe(edit('user'));
    await user.click(edit('mode'));
    await user.click(within(dialog('mode')).getByRole('button', { name: 'Cancel' }));
    expect(document.activeElement).toBe(edit('mode'));
    await user.click(edit('mode'));
    await user.click(within(dialog('mode')).getByRole('button', { name: 'Delete field' }));
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add sub-field' }));
  });
});

describe('VariableEditor — used in', () => {
  it('shows where the opened variable is overridden', async () => {
    const user = userEvent.setup();
    const usage: FieldUsage = { region: [{ kind: 'schedule', id: 's1', name: 'nightly' }] };
    render(<EditorHarness variant="template" usage={usage} initial={{ fields: [{ name: 'region', type: 'string' }, { name: 'port', type: 'int' }] }} />);
    await user.click(screen.getByRole('button', { name: /^region\b/ }));
    expect(within(dialog('region')).getByRole('region', { name: 'Used in' })).toHaveTextContent('Schedule nightly');
    await user.click(within(dialog('region')).getByRole('button', { name: 'Cancel' }));
    await user.click(screen.getByRole('button', { name: /^port\b/ }));
    expect(within(dialog('port')).getByText('Not overridden anywhere')).toBeInTheDocument();
  });

  it('shows nothing without usage', async () => {
    const user = userEvent.setup();
    render(<EditorHarness variant="template" initial={{ fields: [{ name: 'region', type: 'string' }] }} />);
    await user.click(screen.getByRole('button', { name: /^region\b/ }));
    expect(within(dialog('region')).queryByText('Used in')).not.toBeInTheDocument();
  });

  it('closes the field dialog before it opens the webhooks', async () => {
    const user = userEvent.setup();
    const usage: FieldUsage = { region: [{ kind: 'webhook', id: 'w1', name: 'deploy-hook' }] };
    let fieldDialogOpen: boolean | null = null;
    const onOpenWebhooks = () => {
      fieldDialogOpen = screen.queryByRole('dialog', { name: 'Field region' }) !== null;
    };
    render(<EditorHarness variant="template" usage={usage} usageLinks={{ org: 'acme', onOpenWebhooks }} initial={{ fields: [{ name: 'region', type: 'string' }] }} />);
    await user.click(screen.getByRole('button', { name: /^region\b/ }));
    await user.click(within(dialog('region')).getByRole('button', { name: 'Webhook deploy-hook' }));
    expect(screen.queryByRole('dialog', { name: 'Field region' })).not.toBeInTheDocument();
    expect(fieldDialogOpen).toBe(false);
  });
});
