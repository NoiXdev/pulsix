import { useState } from 'react';
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { Select } from '../../../ui/Select.tsx';
import { childPath, indexPath, inputId, problemAt } from './paths.ts';
import { isKeepMarker, isRecord, type KeepMarker } from './secrets.ts';
import type { FieldType, Problem, SchemaField, Vars } from './types.ts';
import { useT } from '../../../i18n/useT.ts';

export interface SchemaFormProps {
  fields: readonly SchemaField[];
  value: Vars;
  onChange: (next: Vars) => void;
  /** Problems with server paths (vhosts[0].domain), shown at their field. */
  problems?: readonly Problem[];
  disabled?: boolean;
  /** Distinguishes input ids when two forms share a page. */
  idPrefix?: string;
  /** Path of this (nested) form inside the document. */
  pathPrefix?: string;
}

/**
 * SchemaForm renders a form for any variable schema (spec §6) as a
 * controlled component over a plain value tree. It never validates: the
 * server does, and its problems come back through `problems` at the same
 * paths this component uses for its inputs.
 */
export function SchemaForm({ fields, value, onChange, problems, disabled, idPrefix = 'sf', pathPrefix = '' }: SchemaFormProps) {
  const t = useT();
  if (fields.length === 0) return <p className="text-xs text-muted-foreground">{t.inventory.form.noFields}</p>;
  return (
    <div className="flex flex-col gap-3">
      {fields.map((f) => (
        <FieldInput
          key={f.name}
          field={f}
          path={childPath(pathPrefix, f.name)}
          value={value[f.name]}
          onChange={(v) => onChange(withKey(value, f.name, v))}
          problems={problems}
          disabled={disabled}
          idPrefix={idPrefix}
        />
      ))}
    </div>
  );
}

function withKey(obj: Vars, key: string, v: unknown): Vars {
  const next = { ...obj };
  if (v === undefined) delete next[key];
  else next[key] = v;
  return next;
}

interface FieldInputProps {
  field: SchemaField;
  path: string;
  value: unknown;
  onChange: (v: unknown) => void;
  problems?: readonly Problem[];
  disabled?: boolean;
  idPrefix: string;
}

function FieldInput(props: FieldInputProps) {
  const { field, path, value, onChange, problems, disabled, idPrefix } = props;
  const id = inputId(idPrefix, path);
  const error = problemAt(problems, path);
  const label = field.label || field.name;

  if (field.type === 'repeater') return <RepeaterInput {...props} label={label} error={error} />;
  if (field.type === 'list') return <ListInput {...props} id={id} label={label} error={error} />;
  if (field.type === 'object') {
    return (
      <fieldset className="flex flex-col gap-2 rounded-md border border-border p-3" disabled={disabled}>
        <legend className="px-1 text-[11px] font-semibold">{label}</legend>
        <SchemaForm
          fields={field.fields ?? []}
          value={isRecord(value) ? value : {}}
          onChange={(v) => onChange(Object.keys(v).length === 0 ? undefined : v)}
          problems={problems}
          disabled={disabled}
          idPrefix={idPrefix}
          pathPrefix={path}
        />
        <FieldFooter field={field} error={error} />
      </fieldset>
    );
  }
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-1">
        <Label htmlFor={id}>{label}</Label>
        {field.required && (
          <span aria-hidden="true" className="text-[11px] text-destructive">
            *
          </span>
        )}
      </div>
      {field.type === 'secret' ? (
        <SecretInput id={id} value={value} onChange={onChange} disabled={disabled} invalid={!!error} />
      ) : (
        <ScalarInput id={id} field={field} value={value} onChange={onChange} disabled={disabled} invalid={!!error} />
      )}
      <FieldFooter field={field} error={error} />
    </div>
  );
}

function FieldFooter({ field, error }: { field: SchemaField; error?: string }) {
  return (
    <>
      {field.description && <span className="text-[11px] text-muted-foreground">{field.description}</span>}
      {error && (
        <span role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      )}
    </>
  );
}

export interface ScalarInputProps {
  id: string;
  field: SchemaField;
  value: unknown;
  onChange: (v: unknown) => void;
  disabled?: boolean;
  invalid?: boolean;
  ariaLabel?: string;
}

/** ScalarInput edits one string, int, float, bool or enum value. An
 * emptied input reports undefined so the key is dropped. */
interface CommonProps {
  id: string;
  disabled?: boolean;
  'aria-invalid'?: boolean;
  'aria-label'?: string;
}

export function ScalarInput({ id, field, value, onChange, disabled, invalid, ariaLabel }: ScalarInputProps) {
  const t = useT();
  const common: CommonProps = { id, disabled, 'aria-invalid': invalid ? true : undefined, 'aria-label': ariaLabel };
  switch (field.type) {
    case 'enum':
      return (
        <Select
          id={id}
          disabled={disabled}
          invalid={invalid}
          aria-label={ariaLabel}
          options={(field.options ?? []).map((o) => ({ value: o, label: o }))}
          value={typeof value === 'string' ? value : ''}
          onChange={(v) => onChange(v === '' ? undefined : v)}
          placeholder={field.default !== undefined ? t.inventory.form.defaultValue(String(field.default)) : '—'}
        />
      );
    case 'int':
    case 'float':
      return <NumberInput common={common} field={field} value={value} onChange={onChange} />;
    case 'bool':
      return (
        <input
          {...common}
          type="checkbox"
          className="h-4 w-4 accent-[var(--px-accent)]"
          checked={value === undefined ? field.default === true : value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
      );
    default:
      return (
        <Input
          {...common}
          type="text"
          className="font-mono"
          value={typeof value === 'string' ? value : ''}
          placeholder={typeof field.default === 'string' ? field.default : undefined}
          onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
        />
      );
  }
}

function parseNumber(raw: string): number | undefined {
  if (raw.trim() === '') return undefined;
  const n = Number(raw);
  return Number.isFinite(n) ? n : undefined;
}

function formatNumber(v: unknown): string {
  return typeof v === 'number' ? String(v) : '';
}

/**
 * NumberInput is a text input with its own text state: "0." or "-" are
 * legitimate steps while typing a number, and a controlled <input
 * type="number"> would wipe them (the browser reports them as ""). The text
 * follows the value only when the value changes to something the text does
 * not already say (a reload, a reset), the "adjust state when a prop
 * changes" pattern from the React documentation — a render-time setState
 * guarded by comparing against the previous value, not an effect (an
 * effect that only mirrors a prop into state is exactly what
 * eslint-plugin-react-hooks 7's `set-state-in-effect` rule rejects).
 */
function NumberInput({ common, field, value, onChange }: { common: CommonProps; field: SchemaField; value: unknown; onChange: (v: unknown) => void }) {
  const [text, setText] = useState(() => formatNumber(value));
  const [seen, setSeen] = useState(value);
  if (!Object.is(value, seen)) {
    setSeen(value);
    if (parseNumber(text) !== value) setText(formatNumber(value));
  }
  return (
    <Input
      {...common}
      type="text"
      inputMode={field.type === 'int' ? 'numeric' : 'decimal'}
      className="font-mono"
      value={text}
      placeholder={field.default !== undefined ? String(field.default) : undefined}
      onChange={(e) => {
        setText(e.target.value);
        onChange(parseNumber(e.target.value));
      }}
    />
  );
}

function SecretInput({ id, value, onChange, disabled, invalid }: { id: string; value: unknown; onChange: (v: unknown) => void; disabled?: boolean; invalid?: boolean }) {
  const t = useT();
  // The marker this input started with, so "Keep stored value" can undo a
  // replacement. The entry's component instance moves with the entry
  // (RepeaterInput keys entries stably), so this stays the right marker.
  const [original] = useState<KeepMarker | null>(() => (isKeepMarker(value) ? value : null));
  if (isKeepMarker(value)) {
    return (
      <div className="flex items-center gap-2">
        <Input
          id={id}
          type="text"
          readOnly
          disabled={disabled}
          aria-invalid={invalid ? true : undefined}
          className="font-mono text-muted-foreground"
          value={t.inventory.form.stored}
        />
        <Button type="button" variant="ghost" disabled={disabled} onClick={() => onChange(undefined)}>
          {t.inventory.form.replace}
        </Button>
      </div>
    );
  }
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type="password"
        autoComplete="new-password"
        className="font-mono"
        disabled={disabled}
        aria-invalid={invalid ? true : undefined}
        value={typeof value === 'string' ? value : ''}
        onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
      />
      {original && (
        <Button type="button" variant="ghost" disabled={disabled} onClick={() => onChange(original)}>
          {t.inventory.form.keepStored}
        </Button>
      )}
    </div>
  );
}

function emptyScalar(type: FieldType): unknown {
  if (type === 'bool') return false;
  if (type === 'int' || type === 'float') return null;
  return '';
}

function ListInput({ field, path, value, onChange, problems, disabled, idPrefix, id, label, error }: FieldInputProps & { id: string; label: string; error?: string }) {
  const t = useT();
  const items = Array.isArray(value) ? (value as unknown[]) : [];
  const item: SchemaField = field.item ?? { name: '', type: 'string' };
  const set = (next: unknown[]) => onChange(next.length === 0 && !field.required ? undefined : next);
  const full = field.max_items !== undefined && items.length >= field.max_items;
  return (
    <div className="flex flex-col gap-1" role="group" aria-labelledby={id}>
      <span id={id} className="text-[11px] font-medium text-muted-foreground">
        {label}
      </span>
      {items.map((it, i) => (
        <div key={i} className="flex items-center gap-2">
          <ScalarInput
            id={inputId(idPrefix, indexPath(path, i))}
            field={item}
            value={it}
            ariaLabel={`${label} ${i + 1}`}
            disabled={disabled}
            invalid={!!problemAt(problems, indexPath(path, i))}
            onChange={(v) => set(items.map((x, j) => (j === i ? (v ?? emptyScalar(item.type)) : x)))}
          />
          <Button type="button" variant="ghost" aria-label={t.inventory.form.removeItem(label, i + 1)} disabled={disabled} onClick={() => set(items.filter((_, j) => j !== i))}>
            <Trash2 size={14} />
          </Button>
        </div>
      ))}
      <Button type="button" variant="ghost" className="self-start" disabled={disabled || full} onClick={() => set([...items, emptyScalar(item.type)])}>
        <Plus size={14} /> {t.inventory.form.add(label)}
      </Button>
      <FieldFooter field={field} error={error} />
    </div>
  );
}

function newKey(): string {
  return Math.random().toString(36).slice(2);
}

function RepeaterInput({ field, path, value, onChange, problems, disabled, idPrefix, label, error }: FieldInputProps & { label: string; error?: string }) {
  const t = useT();
  const items = Array.isArray(value) ? (value as unknown[]) : [];
  // Stable keys move with their entries, so an entry's inputs (and a
  // secret's remembered marker) follow it when entries are reordered. A
  // length change from outside (a reload) falls back to index keys.
  const [keys, setKeys] = useState<string[]>(() => items.map(newKey));
  const stable = keys.length === items.length ? keys : items.map((_, i) => `i${i}`);
  const set = (next: unknown[], nextKeys: string[]) => {
    setKeys(nextKeys);
    onChange(next.length === 0 && !field.required ? undefined : next);
  };
  const move = (from: number, to: number) => {
    const next = [...items];
    const k = [...stable];
    next.splice(to, 0, next.splice(from, 1)[0]);
    k.splice(to, 0, k.splice(from, 1)[0]);
    set(next, k);
  };
  const full = field.max_items !== undefined && items.length >= field.max_items;
  const headingId = inputId(idPrefix, path);

  return (
    <div className="flex flex-col gap-2" role="group" aria-labelledby={headingId}>
      <div className="flex items-center justify-between">
        <span id={headingId} className="text-[11px] font-semibold">
          {label}
          {field.required && <span aria-hidden="true" className="text-destructive"> *</span>}
        </span>
        <span className="text-[11px] text-muted-foreground">
          {t.inventory.form.entries(items.length)}
        </span>
      </div>
      {items.map((it, i) => {
        const entryId = `${headingId}-${stable[i]}`;
        return (
          <div key={stable[i]} role="group" aria-labelledby={entryId} className="rounded-md border border-border p-3">
            <div className="mb-2 flex items-center justify-between">
              <span id={entryId} className="text-[11px] font-semibold">
                {label} {i + 1}
              </span>
              <div className="flex gap-1">
                <Button type="button" variant="ghost" aria-label={t.inventory.form.moveUp(label, i + 1)} disabled={disabled || i === 0} onClick={() => move(i, i - 1)}>
                  <ArrowUp size={14} />
                </Button>
                <Button type="button" variant="ghost" aria-label={t.inventory.form.moveDown(label, i + 1)} disabled={disabled || i === items.length - 1} onClick={() => move(i, i + 1)}>
                  <ArrowDown size={14} />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  aria-label={t.inventory.form.removeItem(label, i + 1)}
                  disabled={disabled}
                  onClick={() => set(items.filter((_, j) => j !== i), stable.filter((_, j) => j !== i))}
                >
                  <Trash2 size={14} />
                </Button>
              </div>
            </div>
            <SchemaForm
              fields={field.fields ?? []}
              value={isRecord(it) ? it : {}}
              onChange={(v) => set(items.map((x, j) => (j === i ? v : x)), stable)}
              problems={problems}
              disabled={disabled}
              idPrefix={idPrefix}
              pathPrefix={indexPath(path, i)}
            />
          </div>
        );
      })}
      <Button type="button" variant="default" className="self-start" disabled={disabled || full} onClick={() => set([...items, {}], [...stable, newKey()])}>
        <Plus size={14} /> {t.inventory.form.add(label)}
      </Button>
      <FieldFooter field={field} error={error} />
    </div>
  );
}
