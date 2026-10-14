import { useCallback, useId, useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useCheckRule, useCreateRule, useUpdateRule } from '../../api/hooks/useRules.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useHostItems } from '../../api/hooks/useItems.ts';
import { RuleForm } from './RuleForm.tsx';
import { RuleExpression } from './RuleExpression.tsx';
import type { MonitoringGroup, Rule, RuleFormValue, RuleInput, RuleMetric, RuleScope } from '../../api/types.ts';

// Still used by RuleHostPicker's own search <input> below, which stays a
// plain text field rather than becoming a Select.
const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground disabled:opacity-50';

/** lines turns a textarea's content into the string list the API takes,
 * dropping blank lines so a trailing newline is not a label of its own —
 * the same helper CheckDialog's units/mounts fields use. */
function lines(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** defaultOpFor picks the operator a freshly chosen metric starts with:
 * the first (and most common) comparison for its kind. */
function defaultOpFor(kind: 'numeric' | 'discrete'): string {
  return kind === 'discrete' ? '==' : '>';
}

const CONFIRM_SWITCH =
  'Switching to the expression editor is a one-way door from here. The way back to the form editor only stays open while the ' +
  "expression keeps exactly the shape the form produces — one comparison against this metric, no arithmetic, no time window, no \"or\". " +
  'Once it leaves that shape, this rule can only be edited as an expression from now on. Continue?';

/** confirmDropClear is the second, separate confirmation a rule with a
 * clear condition has to pass before it is converted.
 *
 * The expression grammar has no syntax for a clear condition at all, and
 * the server stores clear_ast = NULL for every expression-mode rule, so the
 * conversion does not merely close the way back — it deletes the
 * hysteresis. A rule triggering at >= 85 and clearing at < 80 would start
 * flipping between ok and warn on every crossing of 85, which is the exact
 * behaviour the clear condition was written to prevent, with nothing on
 * screen recording that it happened and no way to recover it by reopening
 * the dialog.
 *
 * It names the condition rather than mentioning one in the abstract, and it
 * is asked every time rather than once per dialog (unlike CONFIRM_SWITCH):
 * this one is destructive, and the operator may well have added the clear
 * condition after the first switch. */
function confirmDropClear(op: string, value: string): string {
  return (
    `This rule clears at "${op} ${value}". The expression editor has no syntax for a clear condition, so saving as an ` +
    'expression will remove it — the rule will then change state on every crossing of its threshold instead of holding ' +
    'until the clear condition is met. Remove the clear condition and continue?'
  );
}

export interface RuleDialogProps {
  org: string;
  /** The rule being edited; undefined creates one. */
  rule?: Rule;
  /** The metric catalogue GET /rules/metrics serves — the only honest
   * source for the picker, and for telling a numeric metric from a
   * discrete one. */
  metrics: RuleMetric[];
  /** The organization's monitoring groups, for a group-scoped rule's
   * target. */
  groups: MonitoringGroup[];
  onClose: () => void;
}

/**
 * RuleDialog creates or edits a rule, in whichever mode it already carries.
 *
 * A seeded rule (origin "seed") keeps its metric, scope and severity fixed
 * — the server refuses a change to any of the three (internal/rules
 * Service.Update) — so this dialog disables exactly those three fields and
 * leaves everything else editable: the threshold (RuleForm), the clear
 * condition, the duration and the enabled flag. It also has no mode
 * switch: converting a seeded default into a free-form expression is not a
 * "threshold" edit, so that door stays closed for a seeded row entirely.
 *
 * The mode switch itself only ever runs forward through a confirmation
 * (form → expression), and through a second one naming the clear condition
 * by value when the rule has one: the grammar cannot express a clear
 * condition, so converting deletes it, and that is a change to what the
 * rule DOES rather than only to how it is edited.
 *
 * Nothing in this dialog decodes an edited expression
 * back into form fields — the fields the form editor showed before the
 * switch simply stay held in state, untouched, so "switch back to form"
 * is a plain mode flip, not an attempt to parse whatever has since been
 * typed (which would mean duplicating internal/rules' own parser here and
 * drifting from it). That "way back" is offered only while the live
 * validate result on the current expression still reports form_ready —
 * once the expression leaves the form's subset, the button disappears
 * rather than silently discarding whatever the operator typed.
 */
export function RuleDialog({ org, rule, metrics, groups, onClose }: RuleDialogProps) {
  const editing = !!rule;
  const seeded = editing && rule.origin === 'seed';
  const create = useCreateRule(org);
  const update = useUpdateRule(org, rule?.id ?? '');
  const checkRule = useCheckRule(org);

  const [name, setName] = useState(rule?.name ?? '');
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [scope, setScope] = useState<RuleScope>(rule?.scope ?? 'org');
  const [groupId, setGroupId] = useState(rule?.group_id ?? groups[0]?.id ?? '');
  const [hostId, setHostId] = useState(rule?.host_id ?? '');
  const [item, setItem] = useState(rule?.item ?? '');
  const [metric, setMetric] = useState(rule?.metric ?? metrics[0]?.name ?? '');
  const [severity, setSeverity] = useState<Rule['severity']>(rule?.severity ?? 'warn');
  const [forSeconds, setForSeconds] = useState(String(rule?.for_seconds ?? 0));
  const [mode, setMode] = useState<Rule['mode']>(rule?.mode ?? 'form');
  const [warned, setWarned] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const kind = metrics.find((m) => m.name === metric)?.kind ?? 'numeric';

  const initialForm = rule?.form;
  const [op, setOp] = useState(initialForm?.op ?? defaultOpFor(kind));
  const [value, setValue] = useState(initialForm?.value !== undefined ? String(initialForm.value) : '');
  const [labels, setLabels] = useState((initialForm?.labels ?? []).join('\n'));
  const [clearOp, setClearOp] = useState(initialForm?.clear_op ?? '');
  const [clearValue, setClearValue] = useState(initialForm?.clear_value !== undefined ? String(initialForm.clear_value) : '');

  const [expression, setExpression] = useState(rule?.expression ?? '');
  const [expressionOk, setExpressionOk] = useState(false);
  const [formReady, setFormReady] = useState(false);
  const onExpressionValidChange = useCallback((ok: boolean, ready: boolean) => {
    setExpressionOk(ok);
    setFormReady(ready);
  }, []);

  function onMetricChange(next: string) {
    setMetric(next);
    const nextKind = metrics.find((m) => m.name === next)?.kind ?? 'numeric';
    setOp(defaultOpFor(nextKind));
    setValue('');
    setLabels('');
    setClearOp('');
    setClearValue('');
  }

  /** A discrete metric's label field changes shape with its operator — one
   * line for "==" / "!=", several for "in" — so switching between them
   * clears whatever was typed rather than carrying stale text across into
   * a field of a different shape (a leftover multi-line list sitting in
   * the single-label input, embedded newline and all). A numeric metric's
   * value is untouched by its own operator changing: "> 90" to "< 90"
   * has every reason to keep the same threshold. */
  function onOpChange(next: string) {
    setOp(next);
    if (kind === 'discrete') setLabels('');
  }

  function formBody(): RuleFormValue {
    if (kind === 'discrete') {
      return { metric, op, labels: op === 'in' ? lines(labels) : labels.trim() ? [labels.trim()] : [] };
    }
    return { metric, op, value: Number(value), ...(clearOp ? { clear_op: clearOp, clear_value: Number(clearValue) } : {}) };
  }

  function commonBody(): Omit<RuleInput, 'mode' | 'form' | 'expression'> {
    return {
      name: name.trim(),
      enabled,
      scope,
      ...(scope === 'group' ? { group_id: groupId } : {}),
      ...(scope === 'host' || scope === 'item' ? { host_id: hostId } : {}),
      ...(scope === 'item' ? { item: item.trim() } : {}),
      metric,
      severity,
      for_seconds: Number(forSeconds),
    };
  }

  function body(): RuleInput {
    const common = commonBody();
    return mode === 'form' ? { ...common, mode: 'form', form: formBody() } : { ...common, mode: 'expression', expression };
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      if (editing) await update.mutateAsync(body());
      else await create.mutateAsync(body());
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  async function switchToExpression() {
    if (!warned) {
      if (!confirm(CONFIRM_SWITCH)) return;
      setWarned(true);
    }
    // Only a numeric metric can carry one: formBody puts clear_op/
    // clear_value on the body for the numeric shape alone.
    if (kind !== 'discrete' && clearOp !== '' && !confirm(confirmDropClear(clearOp, clearValue))) return;
    try {
      const checked = await checkRule.mutateAsync({ ...commonBody(), mode: 'form', form: formBody() });
      setExpression(checked.source ?? '');
    } catch {
      // Whatever is wrong will show up the moment the expression editor's
      // own debounced validate runs next — the switch itself must not
      // silently fail closed and leave the dialog stuck.
      setExpression('');
    }
    setMode('expression');
  }

  const currentGroupMissing = groupId !== '' && !groups.some((g) => g.id === groupId);
  const title = editing ? `Edit ${rule.name}` : 'New rule';
  const saveDisabled = create.isPending || update.isPending || (mode === 'expression' && !expressionOk);

  return (
    <Dialog open onClose={onClose} title={title} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <div className="flex flex-col gap-1">
          <Label htmlFor="rule-name">Name</Label>
          <Input id="rule-name" value={name} required maxLength={100} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="grid grid-cols-3 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-metric">Metric</Label>
            <Select
              id="rule-metric"
              value={metric}
              disabled={seeded}
              onChange={onMetricChange}
              // No hint here: the metric catalogue has no name besides this
              // one (RuleMetricDTO carries only `name` and `check_key`), and
              // check_key is always a prefix of name ("cpu" of
              // "cpu.used_pct") — decoration, not disambiguation, since no
              // two rows ever read the same (Move 2 only adds a hint where
              // they could).
              options={metrics.map((m) => ({ value: m.name, label: m.name }))}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-severity">Severity</Label>
            <Select
              id="rule-severity"
              value={severity}
              disabled={seeded}
              onChange={(v) => setSeverity(v as Rule['severity'])}
              options={[
                { value: 'warn', label: 'Warning' },
                { value: 'crit', label: 'Critical' },
              ]}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-for">For (seconds)</Label>
            <Input id="rule-for" type="number" min={0} max={86400} required value={forSeconds} onChange={(e) => setForSeconds(e.target.value)} />
          </div>
        </div>
        {seeded && (
          <p className="text-[11px] text-muted-foreground">
            This rule ships with every organization; its metric, scope and severity are fixed. Its threshold, clear condition, duration and
            enabled state are editable here.
          </p>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor="rule-scope">Applies to</Label>
          <Select
            id="rule-scope"
            value={scope}
            disabled={seeded}
            onChange={(v) => setScope(v as RuleScope)}
            options={[
              { value: 'org', label: 'The whole organization' },
              { value: 'group', label: `A monitoring group${groups.length === 0 ? ' (none yet)' : ''}`, disabled: groups.length === 0 },
              { value: 'host', label: 'One host' },
              { value: 'item', label: 'One item on a host' },
            ]}
          />
        </div>
        {scope === 'group' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="rule-group">Monitoring group</Label>
            <Select
              id="rule-group"
              placeholder="Choose a monitoring group…"
              value={groupId}
              onChange={setGroupId}
              options={[
                // The rule's own binding, when the loaded list does not
                // contain it — a failed GET /monitoring-groups is enough to
                // empty that list, and without this option Select would
                // fall back to "<id> (unavailable)" (the same review finding
                // CheckDialog's own group select fixed).
                ...(currentGroupMissing ? [{ value: groupId, label: `group ${groupId} (current binding)` }] : []),
                ...groups.map((g) => ({ value: g.id, label: g.name })),
              ]}
            />
          </div>
        )}
        {(scope === 'host' || scope === 'item') && <RuleHostPicker org={org} value={hostId} onChange={setHostId} />}
        {scope === 'item' && (
          <RuleItemPicker
            org={org}
            hostId={hostId}
            checkKey={metrics.find((m) => m.name === metric)?.check_key}
            value={item}
            onChange={setItem}
          />
        )}
        {mode === 'form' ? (
          <RuleForm
            kind={kind}
            op={op}
            onOpChange={onOpChange}
            value={value}
            onValueChange={setValue}
            labels={labels}
            onLabelsChange={setLabels}
            clearOp={clearOp}
            onClearOpChange={setClearOp}
            clearValue={clearValue}
            onClearValueChange={setClearValue}
          />
        ) : (
          <RuleExpression org={org} value={expression} onChange={setExpression} base={commonBody()} onValidChange={onExpressionValidChange} />
        )}
        {!seeded && mode === 'form' && (
          <Button type="button" variant="ghost" className="self-start" onClick={() => void switchToExpression()}>
            Switch to expression editor
          </Button>
        )}
        {!seeded && mode === 'expression' && formReady && (
          <Button type="button" variant="ghost" className="self-start" onClick={() => setMode('form')}>
            Switch to form editor
          </Button>
        )}
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Enabled
        </label>
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={saveDisabled}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/**
 * RuleHostPicker chooses the host a host- or item-scoped rule targets.
 * Mirrors CheckDialog's own HostPicker in shape (features/checks): the
 * hosts list is paginated, so it searches through the API rather than
 * filtering one page, and the host a rule already targets stays
 * selectable even when a search does not happen to return it — the same
 * review finding that fixed CheckDialog's binding select (a select whose
 * value matches no option falls back to the empty placeholder and
 * swallows Save with no message).
 *
 * RuleDTO does carry host_name now (backlog 3.30), the same enrichment a
 * check definition has, but only on the rule this dialog already loaded,
 * not on an id this picker's own paginated host search resolves — the
 * editable value here is a plain host id, so a missing current binding
 * (one the search does not happen to return) is still shown by its
 * shortened id rather than a name.
 */
function RuleHostPicker({ org, value, onChange }: { org: string; value: string; onChange: (id: string) => void }) {
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { monitored: true, q });
  const listed = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const currentMissing = value !== '' && !listed.some((h) => h.id === value);

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="rule-host">Host</Label>
      <input
        type="search"
        aria-label="Search monitored hosts"
        placeholder="Search monitored hosts…"
        className={SELECT}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <Select
        id="rule-host"
        placeholder="Choose a monitored host…"
        value={value}
        onChange={onChange}
        options={[
          ...(currentMissing ? [{ value, label: `${value.slice(0, 8)} (current binding)` }] : []),
          ...listed.map((h) => ({ value: h.id, label: h.name })),
        ]}
      />
      {hosts.hasNextPage && <span className="text-[11px] text-muted-foreground">More hosts match than are listed — narrow the search.</span>}
      {!hosts.data && hosts.isError && (
        <span role="alert" className="text-[11px] text-destructive">
          Could not load monitored hosts. {hosts.error instanceof Error ? hosts.error.message : ''}
        </span>
      )}
    </div>
  );
}

/**
 * RuleItemPicker names the item an item-scoped rule targets: a `<datalist>`
 * over the chosen host's discovered items (GET /hosts/{id}/items), with
 * the field underneath it a plain text input throughout (v0.3b-3 spec,
 * "the rule editor's item field becomes a picker ... with free text still
 * accepted"). A rule may name an item that has not appeared yet — the
 * same decoupling ItemDecisionDialog's own pattern field has from the
 * items it matches — so this never becomes a closed `<select>`: a
 * `<datalist>` suggests without restricting what can be typed or
 * submitted.
 *
 * `checkKey` narrows the suggestions to the check the rule's own metric
 * belongs to (RuleMetric's own check_key) — a mount and a unit are never
 * the same kind of item, and listing every item this host has ever
 * reported, regardless of check, would suggest ones the rule's metric
 * could not possibly apply to.
 *
 * The `<datalist>`'s id is generated per instance with useId rather than a
 * fixed string: a `list=` attribute resolves by id against the whole
 * document, and two mounted pickers sharing one id would silently collide
 * — the browser binds to whichever datalist it finds first, offering one
 * picker's suggestions on the other's input, with no error to notice it by.
 */
function RuleItemPicker({
  org,
  hostId,
  checkKey,
  value,
  onChange,
}: {
  org: string;
  hostId: string;
  checkKey: string | undefined;
  value: string;
  onChange: (item: string) => void;
}) {
  const items = useHostItems(org, hostId, hostId !== '');
  const options = [...new Set((items.data?.items ?? []).filter((it) => it.check_key === checkKey).map((it) => it.item))].sort();
  const optionsId = useId();

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="rule-item">Item</Label>
      <Input
        id="rule-item"
        required
        list={optionsId}
        placeholder="/var, sshd, …"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <datalist id={optionsId}>
        {options.map((name) => (
          <option key={name} value={name} />
        ))}
      </datalist>
    </div>
  );
}
