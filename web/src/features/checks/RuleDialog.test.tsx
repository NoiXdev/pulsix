import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { RuleDialog } from './RuleDialog.tsx';
import type { Rule, RuleMetric } from '../../api/types.ts';

const METRICS: RuleMetric[] = [
  { name: 'cpu.used_pct', check_key: 'cpu', kind: 'numeric' },
  { name: 'mem.used_pct', check_key: 'memory', kind: 'numeric' },
];

const expressionRule = (over: Partial<Rule> = {}): Rule => ({
  id: 'r5',
  name: 'Custom expression rule',
  enabled: true,
  origin: 'user',
  scope: 'org',
  check_key: 'cpu',
  metric: 'cpu.used_pct',
  severity: 'warn',
  mode: 'expression',
  expression: 'cpu.used_pct > 90',
  source: 'cpu.used_pct > 90',
  for_seconds: 0,
  created_at: '',
  updated_at: '',
  ...over,
});

const seededRule = (over: Partial<Rule> = {}): Rule => ({
  id: 'r1',
  name: 'CPU usage warning',
  enabled: true,
  origin: 'seed',
  scope: 'org',
  check_key: 'cpu',
  metric: 'cpu.used_pct',
  severity: 'warn',
  mode: 'form',
  form: { metric: 'cpu.used_pct', op: '>=', value: 90 },
  source: 'cpu.used_pct >= 90',
  for_seconds: 900,
  created_at: '',
  updated_at: '',
  ...over,
});

function renderDialog(props: Partial<React.ComponentProps<typeof RuleDialog>> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <RuleDialog org="acme" metrics={METRICS} groups={[]} onClose={() => {}} {...props} />
    </QueryClientProvider>,
  );
}

describe('RuleDialog', () => {
  it('disables Save and shows the message at its position while the expression is invalid', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs/acme/rules/validate': () =>
        new Response(JSON.stringify({ detail: 'invalid rule: compare: needs exactly two operands, got 1 (at byte 14)' }), {
          status: 422,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
    });
    renderDialog({ rule: expressionRule() });

    const textarea = screen.getByLabelText('Expression') as HTMLTextAreaElement;
    await user.clear(textarea);
    await user.type(textarea, 'cpu.used_pct >');

    expect(await screen.findByRole('alert')).toHaveTextContent('(at byte 14)');
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
    // The caret lands on the offending byte offset while the field still
    // has focus, so the operator's next keystroke lands where the parser
    // actually stopped.
    expect(textarea.selectionStart).toBe(14);
  });

  it('enables Save once a debounced expression validates', async () => {
    const user = userEvent.setup();
    let validations = 0;
    mockApi({
      'POST /api/v1/orgs/acme/rules/validate': () => {
        validations += 1;
        return { ok: true, form_ready: true, source: 'cpu.used_pct > 5' };
      },
    });
    renderDialog({ rule: expressionRule() });

    const textarea = screen.getByLabelText('Expression') as HTMLTextAreaElement;
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();

    await user.clear(textarea);
    await user.type(textarea, 'cpu.used_pct > 5');

    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled());
    // One validate call for a whole typed expression, not one per
    // keystroke — the 400ms debounce coalesces them.
    expect(validations).toBe(1);
  });

  it('warns once before switching to the expression editor, and not again for the same dialog', async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      'POST /api/v1/orgs/acme/rules/validate': (req: MockRequest) => {
        const body = req.body as { mode: string; expression?: string; form?: { op: string; value: number } };
        const source = body.mode === 'expression' ? body.expression! : `cpu.used_pct ${body.form!.op} ${body.form!.value}`;
        return { ok: true, form_ready: true, source };
      },
    });
    renderDialog();

    await user.type(screen.getByLabelText('Name'), 'High CPU');
    await user.type(screen.getByLabelText('Value'), '90');

    await user.click(screen.getByRole('button', { name: 'Switch to expression editor' }));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    const textarea = await screen.findByLabelText('Expression');
    await waitFor(() => expect(textarea).toHaveValue('cpu.used_pct > 90'));

    // The live validate on the expression itself reports form_ready, which
    // is what opens the way back — not a decoded copy of whatever text is
    // now in the box.
    await user.click(await screen.findByRole('button', { name: 'Switch to form editor' }));
    // The form fields were never discarded: switching back shows exactly
    // what was there before the switch.
    expect(screen.getByLabelText('Value')).toHaveValue(90);
    expect(screen.queryByLabelText('Expression')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Switch to expression editor' }));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(await screen.findByLabelText('Expression')).toBeInTheDocument();

    confirmSpy.mockRestore();
  });

  it('names the clear condition it would delete before switching, and stays in form mode if that is declined', async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, 'confirm');
    mockApi({
      'POST /api/v1/orgs/acme/rules/validate': () => ({ ok: true, form_ready: true, source: 'cpu.used_pct >= 85' }),
    });
    renderDialog();

    await user.type(screen.getByLabelText('Name'), 'High CPU');
    await user.type(screen.getByLabelText('Value'), '85');
    await pickOption(screen, 'Clear condition', { value: '<' });
    await user.type(screen.getByLabelText('Clear value'), '80');

    // The one-way-door warning is accepted; the clear-condition one is not.
    confirmSpy.mockReturnValueOnce(true).mockReturnValueOnce(false);
    await user.click(screen.getByRole('button', { name: 'Switch to expression editor' }));

    expect(confirmSpy).toHaveBeenCalledTimes(2);
    // The second confirmation names the condition by value: the grammar has
    // no syntax for a clear condition, so the conversion deletes it, and
    // the rule would then flip on every crossing of 85.
    const dropMessage = confirmSpy.mock.calls[1][0] as string;
    expect(dropMessage).toContain('< 80');
    expect(dropMessage).toMatch(/clear condition/i);
    // Declined, so nothing changed: still the form editor, clear condition
    // still set.
    expect(screen.queryByLabelText('Expression')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Clear value')).toHaveValue(80);

    // Accepting both goes through, and the extra confirmation is asked
    // again rather than remembered: it is destructive, unlike the one-way
    // door warning, which is only asked once per dialog.
    confirmSpy.mockReturnValue(true);
    await user.click(screen.getByRole('button', { name: 'Switch to expression editor' }));
    expect(confirmSpy).toHaveBeenCalledTimes(3);
    expect(await screen.findByLabelText('Expression')).toBeInTheDocument();

    confirmSpy.mockRestore();
  });

  it('does not ask about a clear condition when the rule has none', async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      'POST /api/v1/orgs/acme/rules/validate': () => ({ ok: true, form_ready: true, source: 'cpu.used_pct > 90' }),
    });
    renderDialog();

    await user.type(screen.getByLabelText('Name'), 'High CPU');
    await user.type(screen.getByLabelText('Value'), '90');
    await user.click(screen.getByRole('button', { name: 'Switch to expression editor' }));

    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(await screen.findByLabelText('Expression')).toBeInTheDocument();

    confirmSpy.mockRestore();
  });

  it('does not switch to the expression editor when the warning is declined', async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
    renderDialog();

    await user.click(screen.getByRole('button', { name: 'Switch to expression editor' }));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(screen.queryByLabelText('Expression')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Metric')).toBeInTheDocument();

    confirmSpy.mockRestore();
  });

  it('lets a seeded rule keep its threshold and enabled state editable but its metric, scope and severity fixed', async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    mockApi({
      'PUT /api/v1/orgs/acme/rules/r1': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { rule: seededRule({ form: { metric: 'cpu.used_pct', op: '>=', value: 95 }, for_seconds: 600 }) };
      },
    });
    renderDialog({ rule: seededRule() });

    expect(screen.getByLabelText('Metric')).toBeDisabled();
    expect(screen.getByLabelText('Severity')).toBeDisabled();
    expect(screen.getByLabelText('Applies to')).toBeDisabled();
    expect(screen.getByLabelText('Value')).not.toBeDisabled();
    expect(screen.getByLabelText('For (seconds)')).not.toBeDisabled();
    expect(screen.getByLabelText('Enabled')).not.toBeDisabled();
    // Converting a seeded default into a free-form expression is not a
    // threshold edit, so that door does not open for a seeded row at all.
    expect(screen.queryByRole('button', { name: 'Switch to expression editor' })).not.toBeInTheDocument();

    await user.clear(screen.getByLabelText('Value'));
    await user.type(screen.getByLabelText('Value'), '95');
    await user.clear(screen.getByLabelText('For (seconds)'));
    await user.type(screen.getByLabelText('For (seconds)'), '600');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(put).toEqual({
        name: 'CPU usage warning',
        enabled: true,
        scope: 'org',
        metric: 'cpu.used_pct',
        severity: 'warn',
        for_seconds: 600,
        mode: 'form',
        form: { metric: 'cpu.used_pct', op: '>=', value: 95 },
      }),
    );
  });

  // v0.3b-3 spec: "The rule editor's item field becomes a picker over the
  // selected host's discovered items, with free text still accepted — a
  // rule may name an item that has not appeared yet, the same decoupling
  // the decisions have."
  it('offers the selected host’s discovered items as a picker, and still accepts a value that has not appeared yet', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'web-1' }], next_cursor: '' },
      'GET /api/v1/orgs/acme/hosts/h1/items': {
        items: [
          { check_key: 'cpu', item: 'core0', first_seen: '', reported: true, decision: 'watch' },
          { check_key: 'cpu', item: 'core1', first_seen: '', reported: true, decision: 'watch' },
          // A different check's item must not pollute this picker's list —
          // the rule's own metric already fixes which check it belongs to.
          { check_key: 'memory', item: 'swap', first_seen: '', reported: true, decision: 'watch' },
        ],
      },
    });
    renderDialog();

    await pickOption(screen, 'Applies to', { value: 'item' });
    await pickOption(screen, 'Host', { value: 'h1' });

    const itemInput = (await screen.findByLabelText('Item')) as HTMLInputElement;
    const listId = itemInput.getAttribute('list');
    expect(listId).toBeTruthy();
    await waitFor(() => expect(document.querySelectorAll(`#${listId} option`).length).toBeGreaterThan(0));
    const options = [...document.querySelectorAll(`#${listId} option`)].map((o) => o.getAttribute('value'));
    expect(options).toEqual(expect.arrayContaining(['core0', 'core1']));
    expect(options).not.toContain('swap');

    // The picker is a suggestion, not a cage: a value nothing has reported
    // yet is still a valid thing to type and submit.
    await user.type(itemInput, '/not-yet-seen');
    expect(itemInput).toHaveValue('/not-yet-seen');
  });

  it("shows the server's own refusal of a failed save, not a generic message", async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs/acme/rules': () =>
        new Response(JSON.stringify({ detail: 'a rule with this name already exists at this scope' }), {
          status: 409,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
    });
    renderDialog();

    await user.type(screen.getByLabelText('Name'), 'High CPU');
    await user.type(screen.getByLabelText('Value'), '90');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('a rule with this name already exists at this scope');
    expect(screen.getByRole('dialog', { name: 'New rule' })).toBeInTheDocument();
  });
});
