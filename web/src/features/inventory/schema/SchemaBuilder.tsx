import { useState } from 'react';
import { Plus } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { Switch } from '../../../ui/switch.tsx';
import { Select } from '../../../ui/Select.tsx';
import { Tag } from '../../../ui/badge.tsx';
import { SchemaForm, ScalarInput } from './SchemaForm.tsx';
import { membershipFields, problemAt, scopeOf } from './paths.ts';
import { FIELD_TYPES, SCALAR_TYPES, changeType, fieldPath, hasProblemUnder, moveField, newField, removeField, replaceField, typeLabel } from './fieldEditor.ts';
import type { FieldType, Problem, SchemaField, VarSchema, Vars } from './types.ts';
import { useT } from '../../../i18n/useT.ts';

export interface SchemaBuilderProps {
  value: VarSchema;
  onChange: (next: VarSchema) => void;
  /** Schema problems from the server (fields[0].fields[2].name). */
  problems?: readonly Problem[];
  /** Server path of this field list: "" at the top, "fields[2]" inside a sub-schema. */
  pathPrefix?: string;
  /** A sub-schema: no scope, no overridable, no preview. */
  nested?: boolean;
  /** The field types this schema may use; FIELD_TYPES by default. A
   * shared job-template definition withholds "secret" — the server
   * refuses a secret-typed field there by construction (a shared
   * definition's extra vars are written into every organization's own
   * column unsealed), and offering a type that is always refused is
   * worse than not offering it (backlog 2.101). */
  types?: readonly FieldType[];
}

function newKey(): string {
  return Math.random().toString(36).slice(2);
}

/** moveField/removeField (fieldEditor.ts) operate on SchemaField[]; the
 * parallel array of stable keys needs the same splice, generically. */
function moveItem<T>(items: readonly T[], from: number, to: number): T[] {
  const next = [...items];
  next.splice(to, 0, next.splice(from, 1)[0]);
  return next;
}

function removeItem<T>(items: readonly T[], index: number): T[] {
  return items.filter((_, i) => i !== index);
}

/**
 * SchemaBuilder edits a group's variable schema (SchemaBuilder mockup):
 * the field list on the left, the selected field's editor in the middle,
 * and, at the top level, the generated assignment form as a live preview.
 */
export function SchemaBuilder({ value, onChange, problems, pathPrefix = '', nested = false, types = FIELD_TYPES }: SchemaBuilderProps) {
  const t = useT();
  const [selected, setSelected] = useState(0);
  const fields = value.fields;
  const index = Math.min(selected, fields.length - 1);
  const field = index >= 0 ? fields[index] : undefined;
  const idBase = `sb-${pathPrefix.replace(/[^a-zA-Z0-9]/g, '-') || 'top'}`;

  // Stable per-field keys, parallel to `fields`, that move with their field
  // on add/move/delete instead of following the field's index. This lets
  // <FieldEditor> be keyed by field identity: moving the selected field
  // (which changes its index, and so the "Move up"/"Move down" button just
  // pressed) does not remount its editor and drop keyboard focus, while
  // selecting a *different* field still mounts a fresh editor (needed so
  // OptionsField, which seeds its text from props, never carries one
  // field's local state into another). A length mismatch — the field list
  // was replaced from outside, e.g. the parent resetting the draft — falls
  // back to index keys for this render; the next add/move/delete resyncs.
  const [keys, setKeys] = useState<string[]>(() => fields.map(newKey));
  const stable = keys.length === fields.length ? keys : fields.map((_, i) => `i${i}`);
  const setFields = (next: SchemaField[], nextKeys: string[]) => {
    setKeys(nextKeys);
    onChange({ fields: next });
  };

  // Problems about this field list itself, not about any one field: "" at
  // the top level (e.g. too many fields), or the sub-schema's own path when
  // nested (e.g. "fields[0]" for the repeater at index 0).
  const listProblems = (problems ?? []).filter((p) => p.path === pathPrefix);

  return (
    <div className={nested ? 'flex flex-col gap-3' : 'grid grid-cols-[minmax(0,200px)_minmax(0,1fr)] items-start gap-4 @min-[52rem]:grid-cols-[240px_minmax(0,1fr)_300px]'}>
      <div className="flex flex-col gap-1">
        {listProblems.length > 0 && (
          <ul role="alert" className="flex flex-col gap-0.5 text-[11px] text-destructive">
            {listProblems.map((p, i) => (
              <li key={i}>{p.message}</li>
            ))}
          </ul>
        )}
        <ul className="flex flex-col gap-1" aria-label={nested ? t.inventory.builder.subFields : t.inventory.builder.fields}>
          {fields.map((f, i) => (
            <li key={i}>
              <button
                type="button"
                aria-pressed={i === index}
                onClick={() => setSelected(i)}
                className={`flex w-full items-center gap-2 rounded-md border px-2.5 py-1.5 text-left ${
                  i === index ? 'border-primary bg-accent' : 'border-border bg-card hover:bg-accent'
                }`}
              >
                <span className="flex-1 truncate font-mono text-xs">{f.name}</span>
                <span className="text-[11px] text-muted-foreground">{typeLabel(f, t)}</span>
                {!nested && scopeOf(f) === 'host' && <Tag>{t.inventory.builder.hostTag}</Tag>}
                {f.required && <span className="text-[11px] text-destructive" aria-label={t.inventory.builder.required}>*</span>}
                {hasProblemUnder(problems, fieldPath(pathPrefix, i)) && (
                  <span className="h-1.5 w-1.5 rounded-full bg-destructive" aria-label={t.inventory.builder.hasProblems} />
                )}
              </button>
            </li>
          ))}
        </ul>
        <Button
          type="button"
          variant="ghost"
          className="self-start"
          onClick={() => {
            setFields([...fields, newField(fields)], [...stable, newKey()]);
            setSelected(fields.length);
          }}
        >
          <Plus size={14} /> {nested ? t.inventory.builder.addSubField : t.inventory.builder.addField}
        </Button>
      </div>

      {field ? (
        <FieldEditor
          // Keyed by the field's stable identity, not its index or path: a
          // different field (selected, or landing at this index because
          // another field was deleted) always gets a fresh editor, but
          // moving the selected field keeps the same key — and so the same
          // mounted editor and DOM focus — because the key travels with it.
          key={stable[index]}
          field={field}
          path={fieldPath(pathPrefix, index)}
          idBase={`${idBase}-${index}`}
          nested={nested}
          types={types}
          problems={problems}
          canMoveUp={index > 0}
          canMoveDown={index < fields.length - 1}
          onChange={(f) => setFields(replaceField(fields, index, f), stable)}
          onMove={(dir) => {
            setFields(moveField(fields, index, index + dir), moveItem(stable, index, index + dir));
            setSelected(index + dir);
          }}
          onRemove={() => {
            setFields(removeField(fields, index), removeItem(stable, index));
            setSelected(Math.max(0, index - 1));
          }}
        />
      ) : (
        <p className="text-xs text-muted-foreground">{t.inventory.builder.noFields}</p>
      )}

      {!nested && <AssignmentPreview schema={value} />}
    </div>
  );
}

interface FieldEditorProps {
  field: SchemaField;
  path: string;
  idBase: string;
  nested: boolean;
  types: readonly FieldType[];
  problems?: readonly Problem[];
  canMoveUp: boolean;
  canMoveDown: boolean;
  onChange: (f: SchemaField) => void;
  onMove: (dir: -1 | 1) => void;
  onRemove: () => void;
}

function FieldEditor({ field, path, idBase, nested, types, problems, canMoveUp, canMoveDown, onChange, onMove, onRemove }: FieldEditorProps) {
  const t = useT();
  const id = (k: string) => `${idBase}-${k}`;
  const problem = (k: string) => problemAt(problems, `${path}.${k}`);
  const set = (patch: Partial<SchemaField>) => onChange({ ...field, ...patch });
  const isText = field.type === 'string';
  const hasBounds = isText || field.type === 'int' || field.type === 'float';
  const isCollection = field.type === 'list' || field.type === 'repeater';
  const isSubSchema = field.type === 'repeater' || field.type === 'object';
  const hasDefault = SCALAR_TYPES.includes(field.type);
  const hasItemOptions = field.type === 'list' && field.item?.type === 'enum';
  const showScope = !nested;
  const showOverridable = !nested && scopeOf(field) === 'group';

  // Every attribute this editor gives its own dedicated slot to, so the
  // catch-all list below never repeats a message that is already shown next
  // to the field it belongs to.
  const shownSuffixes = ['name', 'type'];
  if (showScope) shownSuffixes.push('scope');
  if (showOverridable) shownSuffixes.push('overridable');
  if (field.type === 'enum') shownSuffixes.push('options');
  if (hasBounds) shownSuffixes.push('min', 'max');
  if (isText) shownSuffixes.push('pattern');
  if (isCollection) shownSuffixes.push('min_items', 'max_items');
  if (hasItemOptions) shownSuffixes.push('item.options');
  if (hasDefault) shownSuffixes.push('default');
  if (isSubSchema) shownSuffixes.push('fields');

  // Every other problem at or under this field's own path: an exact match
  // on `path` is the field list's own problem (e.g. too many sub-fields),
  // already shown above the nested list by the recursive SchemaBuilder
  // itself; anything starting with `fields[` belongs to a sub-field, shown
  // by that sub-field's own editor. What is left has nowhere dedicated to
  // go, so it is listed here rather than staying invisible.
  const otherProblems = (problems ?? []).filter((p) => {
    if (!p.path.startsWith(`${path}.`)) return false;
    const suffix = p.path.slice(path.length + 1);
    if (suffix.startsWith('fields[')) return false;
    return !shownSuffixes.includes(suffix);
  });

  return (
    <div role="group" aria-label={t.inventory.builder.fieldGroup(field.name)} className="flex flex-col gap-3 rounded-[10px] border border-border bg-card p-4">
      <div className="grid grid-cols-2 gap-3">
        <TextField id={id('name')} label={t.inventory.builder.name} mono value={field.name} error={problem('name')} onChange={(v) => set({ name: v })} />
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('type')}>{t.inventory.builder.type}</Label>
          <Select
            id={id('type')}
            options={types.map((ty) => ({ value: ty, label: ty === 'object' ? t.inventory.builder.subSchemaObject : ty }))}
            value={field.type}
            onChange={(v) => onChange(changeType(field, v as FieldType))}
          />
          {problem('type') && (
            <span role="alert" className="text-[11px] text-destructive">
              {problem('type')}
            </span>
          )}
        </div>
      </div>
      <TextField id={id('label')} label={t.inventory.builder.label} value={field.label ?? ''} onChange={(v) => set({ label: v || undefined })} />
      <TextField id={id('description')} label={t.inventory.builder.description} value={field.description ?? ''} onChange={(v) => set({ description: v || undefined })} />

      {!nested && (
        <fieldset className="flex flex-col gap-1">
          <legend className="text-[11px] font-medium text-muted-foreground">{t.inventory.builder.scope}</legend>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name={id('scope')} checked={scopeOf(field) === 'group'} onChange={() => set({ scope: undefined })} />
            {t.inventory.builder.scopeGroup}
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name={id('scope')} checked={scopeOf(field) === 'host'} onChange={() => set({ scope: 'host', overridable: undefined })} />
            {t.inventory.builder.scopeHost}
          </label>
          {problem('scope') && (
            <span role="alert" className="text-[11px] text-destructive">
              {problem('scope')}
            </span>
          )}
        </fieldset>
      )}
      <SwitchField id={id('required')} label={t.inventory.builder.requiredLabel} checked={!!field.required} onChange={(c) => set({ required: c || undefined })} />
      {showOverridable && (
        <div className="flex flex-col gap-1">
          <SwitchField id={id('overridable')} label={t.inventory.builder.hostsMayOverride} checked={!!field.overridable} onChange={(c) => set({ overridable: c || undefined })} />
          {problem('overridable') && (
            <span role="alert" className="text-[11px] text-destructive">
              {problem('overridable')}
            </span>
          )}
        </div>
      )}

      {field.type === 'enum' && (
        <OptionsField id={id('options')} value={field.options ?? []} error={problem('options')} onChange={(options) => set({ options, default: undefined })} />
      )}
      {hasBounds && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField id={id('min')} label={isText ? t.inventory.builder.minLength : t.inventory.builder.min} value={field.min} error={problem('min')} onChange={(min) => set({ min })} />
          <NumberField id={id('max')} label={isText ? t.inventory.builder.maxLength : t.inventory.builder.max} value={field.max} error={problem('max')} onChange={(max) => set({ max })} />
        </div>
      )}
      {isText && <TextField id={id('pattern')} label={t.inventory.builder.pattern} mono value={field.pattern ?? ''} error={problem('pattern')} onChange={(v) => set({ pattern: v || undefined })} />}
      {isCollection && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField id={id('min_items')} label={t.inventory.builder.minItems} value={field.min_items} error={problem('min_items')} onChange={(min_items) => set({ min_items })} />
          <NumberField id={id('max_items')} label={t.inventory.builder.maxItems} value={field.max_items} error={problem('max_items')} onChange={(max_items) => set({ max_items })} />
        </div>
      )}
      {field.type === 'list' && (
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('item')}>{t.inventory.builder.itemType}</Label>
          <Select
            id={id('item')}
            options={SCALAR_TYPES.map((ty) => ({ value: ty, label: ty }))}
            value={field.item?.type ?? 'string'}
            onChange={(v) => {
              const t = v as FieldType;
              set({ item: t === 'enum' ? { name: '', type: t, options: field.item?.options ?? ['option_1'] } : { name: '', type: t } });
            }}
          />
        </div>
      )}
      {hasItemOptions && field.item && (
        <OptionsField id={id('item-options')} value={field.item.options ?? []} error={problem('item.options')} onChange={(options) => set({ item: { ...(field.item ?? { name: '', type: 'enum' }), options } })} />
      )}
      {hasDefault && (
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('default')}>{t.inventory.builder.default}</Label>
          <ScalarInput id={id('default')} field={{ ...field, default: undefined, required: false }} value={field.default} onChange={(d) => set({ default: d })} />
          {problem('default') && (
            <span role="alert" className="text-[11px] text-destructive">
              {problem('default')}
            </span>
          )}
        </div>
      )}
      {isSubSchema && (
        <div className="flex flex-col gap-2 border-t border-border pt-3">
          <span className="text-[11px] font-semibold">{t.inventory.builder.subSchemaHeading}</span>
          {problem('fields') && (
            <span role="alert" className="text-[11px] text-destructive">
              {problem('fields')}
            </span>
          )}
          <SchemaBuilder nested types={types} value={{ fields: field.fields ?? [] }} onChange={(v) => set({ fields: v.fields })} problems={problems} pathPrefix={path} />
        </div>
      )}
      {otherProblems.length > 0 && (
        <ul role="alert" className="flex flex-col gap-0.5 text-[11px] text-destructive">
          {otherProblems.map((p, i) => (
            <li key={i}>{p.message}</li>
          ))}
        </ul>
      )}
      <div className="flex gap-2 border-t border-border pt-3">
        <Button type="button" variant="ghost" disabled={!canMoveUp} onClick={() => onMove(-1)}>
          {t.inventory.builder.moveUp}
        </Button>
        <Button type="button" variant="ghost" disabled={!canMoveDown} onClick={() => onMove(1)}>
          {t.inventory.builder.moveDown}
        </Button>
        <Button type="button" variant="destructive" className="ml-auto" onClick={onRemove}>
          {t.inventory.builder.deleteField}
        </Button>
      </div>
    </div>
  );
}

function TextField({ id, label, value, onChange, error, mono }: { id: string; label: string; value: string; onChange: (v: string) => void; error?: string; mono?: boolean }) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} value={value} className={mono ? 'font-mono' : undefined} aria-invalid={error ? true : undefined} onChange={(e) => onChange(e.target.value)} />
      {error && (
        <span role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      )}
    </div>
  );
}

function NumberField({ id, label, value, onChange, error }: { id: string; label: string; value?: number; onChange: (v: number | undefined) => void; error?: string }) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="text"
        inputMode="decimal"
        className="font-mono"
        value={value === undefined ? '' : String(value)}
        aria-invalid={error ? true : undefined}
        onChange={(e) => {
          const raw = e.target.value.trim();
          const n = Number(raw);
          onChange(raw === '' || !Number.isFinite(n) ? undefined : n);
        }}
      />
      {error && (
        <span role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      )}
    </div>
  );
}

function OptionsField({ id, value, onChange, error }: { id: string; value: string[]; onChange: (v: string[]) => void; error?: string }) {
  const t = useT();
  const [text, setText] = useState(value.join('\n'));
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{t.inventory.builder.options}</Label>
      <textarea
        id={id}
        rows={4}
        className="rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-xs text-foreground focus-visible:border-primary focus-visible:outline-none"
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          onChange(e.target.value.split('\n').map((o) => o.trim()).filter((o) => o !== ''));
        }}
      />
      {error && (
        <span role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      )}
    </div>
  );
}

function SwitchField({ id, label, checked, onChange }: { id: string; label: string; checked: boolean; onChange: (c: boolean) => void }) {
  return (
    <div className="flex items-center gap-2 text-xs">
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
      <label htmlFor={id}>{label}</label>
    </div>
  );
}

function AssignmentPreview({ schema }: { schema: VarSchema }) {
  const t = useT();
  const [vars, setVars] = useState<Vars>({});
  return (
    <section aria-label={t.inventory.builder.previewLabel} className="col-span-full flex min-w-0 flex-col gap-3 rounded-[10px] border border-border bg-card p-4 @min-[52rem]:col-span-1">
      <h3 className="text-[13px] font-semibold">{t.inventory.builder.previewHeading}</h3>
      <p className="text-[11px] text-muted-foreground">{t.inventory.builder.previewHint}</p>
      <SchemaForm fields={membershipFields(schema)} value={vars} onChange={setVars} idPrefix="preview" />
    </section>
  );
}
