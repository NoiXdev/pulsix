import { useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useT } from '../../i18n/useT.ts';
import { FieldError } from '../instance/shared/formParts.tsx';
import { assertionOps, isNumericSubject, type AssertionPlugin, type AssertionSubject } from './assertionFormat.ts';
import {
  assertionSubjects, composeRow, composeRows, emptyRow, entriesOf, entryNamedBy, isBlank, parseRows, rowFromEntry, takesArg,
  takesOperand, withOp, withSubject, type AssertionRow,
} from './assertionRows.ts';

const TEXTAREA = 'min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

type Mode = 'rows' | 'text';

function rowsOf(text: string, plugin: AssertionPlugin): AssertionRow[] | null {
  const parsed = parseRows(text, plugin);
  return 'rows' in parsed ? parsed.rows : null;
}

/**
 * AssertionEditor edits a check's `assertions` (backlog 3.13b for http,
 * 3.87 for tcp, dns and tls, each with its own subjects): one row per entry — name, subject with its argument, an operator the
 * subject takes, the value (none for exists and !exists, two bounds for
 * in) and the severity — composing the canonical text the form sends
 * (assertionRows.ts). "Edit as text" shows that text in a textarea, one
 * entry per line, and switching back reads it into rows again; a line the
 * grammar refuses keeps the editor on the text until it is corrected.
 *
 * The form holds the text, never the rows: what is saved is always what
 * the textarea would show. Rows the operator added and left blank are
 * not part of it.
 *
 * Errors land on the row they are about: after a refused save, a row the
 * grammar refuses says so, and an API error that names an entry (the
 * whole quoted entry, or its name) is shown under that entry's row.
 */
export function AssertionEditor({
  plugin = 'http',
  id,
  label,
  hint,
  placeholder,
  value,
  error,
  serverError,
  disabled,
  onChange,
}: {
  /** The check's plugin, whose subjects the rows offer. */
  plugin?: AssertionPlugin;
  id: string;
  label: string;
  hint?: string;
  placeholder?: string;
  /** The list's text, one entry per line. */
  value: string;
  /** The field's own validation message, set after a refused save. */
  error?: string;
  /** The API's refusal of the last save, if any. */
  serverError?: string | null;
  disabled?: boolean;
  onChange: (text: string) => void;
}) {
  const t = useT();
  const msg = t.checks.assertionEditor;
  const [mode, setMode] = useState<Mode>(() => (rowsOf(value, plugin) ? 'rows' : 'text'));
  const [rows, setRows] = useState<AssertionRow[]>(() => rowsOf(value, plugin) ?? []);
  // The text this editor last handed up: a different value arriving is
  // someone else's (the stored definition again), and the rows follow it.
  const [emitted, setEmitted] = useState(value);
  const [toggleError, setToggleError] = useState<string | null>(null);
  if (value !== emitted) {
    setEmitted(value);
    const next = rowsOf(value, plugin);
    if (next) setRows(next);
    else setMode('text');
  }

  function emit(text: string) {
    setEmitted(text);
    onChange(text);
  }

  function update(next: AssertionRow[]) {
    setRows(next);
    emit(composeRows(next));
  }

  const set = (i: number, row: AssertionRow) => update(rows.map((r, j) => (j === i ? row : r)));

  function toRows() {
    const parsed = parseRows(value, plugin);
    if ('bad' in parsed) {
      setToggleError(msg.cannotParse(parsed.bad));
      return;
    }
    setToggleError(null);
    setRows(parsed.rows);
    emit(composeRows(parsed.rows));
    setMode('rows');
  }

  const hintId = hint ? `${id}-hint` : undefined;
  const hasHtml =
    plugin === 'http' && (mode === 'rows' ? rows.some((r) => r.subject === 'html' || r.subject === 'html count') : /\bhtml\(/.test(value));

  // The API's error, on the entry it names (by index among the sent,
  // non-blank rows).
  const sent = rows.map((r, i) => ({ r, i })).filter(({ r }) => !isBlank(r));
  const namedText = entryNamedBy(entriesOf(value), serverError);
  const namedSent = mode === 'rows' ? entryNamedBy(sent.map(({ r }) => composeRow(r)), serverError) : -1;
  const namedRow = namedSent >= 0 ? sent[namedSent].i : -1;

  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center justify-between gap-2">
        <Label id={`${id}-label`} htmlFor={mode === 'text' ? id : undefined}>
          {label}
        </Label>
        <Button
          type="button"
          variant="ghost"
          disabled={disabled}
          onClick={() => {
            if (mode === 'rows') {
              setToggleError(null);
              setMode('text');
            } else {
              toRows();
            }
          }}
        >
          {mode === 'rows' ? msg.editText : msg.editRows}
        </Button>
      </div>
      {mode === 'text' ? (
        <>
          <textarea
            id={id}
            className={TEXTAREA}
            value={value}
            disabled={disabled}
            placeholder={placeholder}
            aria-invalid={error || namedText >= 0 ? true : undefined}
            aria-describedby={hintId}
            onChange={(e) => {
              setToggleError(null);
              emit(e.target.value);
            }}
          />
          {namedText >= 0 && serverError && <FieldError>{serverError}</FieldError>}
          <FieldError>{toggleError ?? undefined}</FieldError>
        </>
      ) : (
        <div role="group" aria-labelledby={`${id}-label`} aria-describedby={hintId} className="flex flex-col gap-2">
          {rows.length === 0 && <p className="text-[11px] text-muted-foreground">{msg.empty}</p>}
          {rows.map((row, i) => (
            <AssertionRowFields
              key={i}
              n={i + 1}
              row={row}
              plugin={plugin}
              disabled={disabled}
              problem={
                i === namedRow && serverError
                  ? serverError
                  : error && !isBlank(row) && !rowFromEntry(composeRow(row), plugin)
                    ? msg.rowInvalid
                    : undefined
              }
              onChange={(next) => set(i, next)}
              onRemove={() => update(rows.filter((_, j) => j !== i))}
            />
          ))}
          <div>
            <Button type="button" variant="ghost" disabled={disabled} onClick={() => setRows([...rows, emptyRow(plugin)])}>
              <Plus size={14} /> {msg.add}
            </Button>
          </div>
        </div>
      )}
      {hint && (
        <p id={hintId} className="text-[11px] text-muted-foreground">
          {hint}
        </p>
      )}
      {hasHtml && <p className="text-[11px] text-muted-foreground">{msg.selectorHint}</p>}
      <FieldError>{error}</FieldError>
    </div>
  );
}

function AssertionRowFields({
  n,
  row,
  plugin,
  disabled,
  problem,
  onChange,
  onRemove,
}: {
  n: number;
  row: AssertionRow;
  plugin: AssertionPlugin;
  disabled?: boolean;
  problem?: string;
  onChange: (row: AssertionRow) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const msg = t.checks.assertionEditor;
  const numeric = isNumericSubject(row.subject);
  const valuePlaceholder = numeric ? msg.numberPlaceholder : msg.textPlaceholder;
  const argPlaceholder =
    row.subject === 'header'
      ? msg.argPlaceholders.header
      : row.subject === 'json'
        ? msg.argPlaceholders.json
        : row.subject === 'answer'
          ? msg.argPlaceholders.answer
          : msg.argPlaceholders.html;
  const invalid = problem ? true : undefined;
  return (
    <div className="flex flex-col gap-1 rounded-md border border-border p-2" data-testid="assertion-row">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={msg.name(n)}
          className="w-28 font-mono"
          placeholder={msg.columns.name}
          maxLength={64}
          disabled={disabled}
          aria-invalid={invalid}
          value={row.name}
          onChange={(e) => onChange({ ...row, name: e.target.value })}
        />
        <div className="w-56">
          <Select
            aria-label={msg.subject(n)}
            disabled={disabled}
            options={assertionSubjects(plugin).map((s) => ({ value: s, label: msg.subjects[s] }))}
            value={row.subject}
            onChange={(v) => onChange(withSubject(row, v as AssertionSubject))}
          />
        </div>
        {takesArg(row.subject) && (
          <Input
            aria-label={msg.arg(n)}
            className="min-w-32 flex-1 font-mono"
            placeholder={argPlaceholder}
            maxLength={256}
            disabled={disabled}
            aria-invalid={invalid}
            value={row.arg}
            onChange={(e) => onChange({ ...row, arg: e.target.value })}
          />
        )}
        <div className="w-28">
          <Select
            aria-label={msg.op(n)}
            disabled={disabled}
            options={assertionOps(row.subject).map((o) => ({ value: o, label: o }))}
            value={row.op}
            onChange={(v) => onChange(withOp(row, v))}
          />
        </div>
        {row.op === 'in' ? (
          <>
            <Input
              aria-label={msg.from(n)}
              className="w-20 font-mono"
              inputMode="decimal"
              placeholder="200"
              disabled={disabled}
              aria-invalid={invalid}
              value={row.operand}
              onChange={(e) => onChange({ ...row, operand: e.target.value })}
            />
            <span className="font-mono text-xs text-muted-foreground">..</span>
            <Input
              aria-label={msg.to(n)}
              className="w-20 font-mono"
              inputMode="decimal"
              placeholder="299"
              disabled={disabled}
              aria-invalid={invalid}
              value={row.hi}
              onChange={(e) => onChange({ ...row, hi: e.target.value })}
            />
          </>
        ) : (
          takesOperand(row.op) && (
            <Input
              aria-label={msg.operand(n)}
              className="min-w-24 flex-1 font-mono"
              inputMode={numeric ? 'decimal' : undefined}
              placeholder={valuePlaceholder}
              maxLength={numeric ? 32 : 500}
              disabled={disabled}
              aria-invalid={invalid}
              value={row.operand}
              onChange={(e) => onChange({ ...row, operand: e.target.value })}
            />
          )
        )}
        <div className="w-28">
          <Select
            aria-label={msg.severity(n)}
            disabled={disabled}
            options={[
              { value: 'crit', label: t.checks.severity.crit },
              { value: 'warn', label: t.checks.severity.warn },
            ]}
            value={row.severity}
            onChange={(v) => onChange({ ...row, severity: v === 'warn' ? 'warn' : 'crit' })}
          />
        </div>
        <Button type="button" variant="ghost" aria-label={msg.remove(n)} disabled={disabled} onClick={onRemove}>
          <Trash2 size={14} />
        </Button>
      </div>
      <FieldError>{problem}</FieldError>
    </div>
  );
}
