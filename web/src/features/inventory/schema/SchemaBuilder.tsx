import { useState } from 'react';
import { Plus } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { Input } from '../../../ui/input.tsx';
import { Label } from '../../../ui/label.tsx';
import { Tag } from '../../../ui/badge.tsx';
import { SchemaForm, ScalarInput } from './SchemaForm.tsx';
import { membershipFields, problemAt, scopeOf } from './paths.ts';
import { FIELD_TYPES, SCALAR_TYPES, changeType, fieldPath, hasProblemUnder, moveField, newField, removeField, replaceField, typeLabel } from './fieldEditor.ts';
import type { FieldType, Problem, SchemaField, VarSchema, Vars } from './types.ts';

export interface SchemaBuilderProps {
  value: VarSchema;
  onChange: (next: VarSchema) => void;
  /** Schema problems from the server (fields[0].fields[2].name). */
  problems?: readonly Problem[];
  /** Server path of this field list: "" at the top, "fields[2]" inside a sub-schema. */
  pathPrefix?: string;
  /** A sub-schema: no scope, no overridable, no preview. */
  nested?: boolean;
}

const SELECT =
  'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground focus-visible:border-primary focus-visible:outline-none';

/**
 * SchemaBuilder edits a group's variable schema (SchemaBuilder mockup):
 * the field list on the left, the selected field's editor in the middle,
 * and, at the top level, the generated assignment form as a live preview.
 */
export function SchemaBuilder({ value, onChange, problems, pathPrefix = '', nested = false }: SchemaBuilderProps) {
  const [selected, setSelected] = useState(0);
  const fields = value.fields;
  const index = Math.min(selected, fields.length - 1);
  const field = index >= 0 ? fields[index] : undefined;
  const setFields = (next: SchemaField[]) => onChange({ fields: next });
  const idBase = `sb-${pathPrefix.replace(/[^a-zA-Z0-9]/g, '-') || 'top'}`;

  return (
    <div className={nested ? 'flex flex-col gap-3' : 'grid grid-cols-[240px_minmax(0,1fr)_300px] items-start gap-4'}>
      <div className="flex flex-col gap-1">
        <ul className="flex flex-col gap-1" aria-label={nested ? 'Sub-fields' : 'Fields'}>
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
                <span className="text-[11px] text-muted-foreground">{typeLabel(f)}</span>
                {!nested && scopeOf(f) === 'host' && <Tag>host</Tag>}
                {f.required && <span className="text-[11px] text-destructive" aria-label="required">*</span>}
                {hasProblemUnder(problems, fieldPath(pathPrefix, i)) && (
                  <span className="h-1.5 w-1.5 rounded-full" style={{ background: 'var(--px-crit)' }} aria-label="has problems" />
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
            setFields([...fields, newField(fields)]);
            setSelected(fields.length);
          }}
        >
          <Plus size={14} /> {nested ? 'Add sub-field' : 'Add field'}
        </Button>
      </div>

      {field ? (
        <FieldEditor
          field={field}
          path={fieldPath(pathPrefix, index)}
          idBase={`${idBase}-${index}`}
          nested={nested}
          problems={problems}
          canMoveUp={index > 0}
          canMoveDown={index < fields.length - 1}
          onChange={(f) => setFields(replaceField(fields, index, f))}
          onMove={(dir) => {
            setFields(moveField(fields, index, index + dir));
            setSelected(index + dir);
          }}
          onRemove={() => {
            setFields(removeField(fields, index));
            setSelected(Math.max(0, index - 1));
          }}
        />
      ) : (
        <p className="text-xs text-muted-foreground">No fields yet.</p>
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
  problems?: readonly Problem[];
  canMoveUp: boolean;
  canMoveDown: boolean;
  onChange: (f: SchemaField) => void;
  onMove: (dir: -1 | 1) => void;
  onRemove: () => void;
}

function FieldEditor({ field, path, idBase, nested, problems, canMoveUp, canMoveDown, onChange, onMove, onRemove }: FieldEditorProps) {
  const id = (k: string) => `${idBase}-${k}`;
  const problem = (k: string) => problemAt(problems, `${path}.${k}`) ?? (k === 'name' ? problemAt(problems, path) : undefined);
  const set = (patch: Partial<SchemaField>) => onChange({ ...field, ...patch });
  const isText = field.type === 'string';
  const hasBounds = isText || field.type === 'int' || field.type === 'float';
  const isCollection = field.type === 'list' || field.type === 'repeater';

  return (
    <div role="group" aria-label={`Field ${field.name}`} className="flex flex-col gap-3 rounded-[10px] border border-border bg-card p-4">
      <div className="grid grid-cols-2 gap-3">
        <TextField id={id('name')} label="Name" mono value={field.name} error={problem('name')} onChange={(v) => set({ name: v })} />
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('type')}>Type</Label>
          <select id={id('type')} className={SELECT} value={field.type} onChange={(e) => onChange(changeType(field, e.target.value as FieldType))}>
            {FIELD_TYPES.map((t) => (
              <option key={t} value={t}>
                {t === 'object' ? 'sub-schema (object)' : t}
              </option>
            ))}
          </select>
        </div>
      </div>
      <TextField id={id('label')} label="Label" value={field.label ?? ''} onChange={(v) => set({ label: v || undefined })} />
      <TextField id={id('description')} label="Description" value={field.description ?? ''} onChange={(v) => set({ description: v || undefined })} />

      {!nested && (
        <fieldset className="flex flex-col gap-1">
          <legend className="text-[11px] font-medium text-muted-foreground">Scope</legend>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name={id('scope')} checked={scopeOf(field) === 'group'} onChange={() => set({ scope: undefined })} />
            Group (set once on the group)
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name={id('scope')} checked={scopeOf(field) === 'host'} onChange={() => set({ scope: 'host', overridable: undefined })} />
            Host (asked when a host is assigned)
          </label>
        </fieldset>
      )}
      <Checkbox id={id('required')} label="Required" checked={!!field.required} onChange={(c) => set({ required: c || undefined })} />
      {!nested && scopeOf(field) === 'group' && (
        <Checkbox id={id('overridable')} label="Hosts may override" checked={!!field.overridable} onChange={(c) => set({ overridable: c || undefined })} />
      )}

      {field.type === 'enum' && (
        <OptionsField id={id('options')} value={field.options ?? []} error={problem('options')} onChange={(options) => set({ options, default: undefined })} />
      )}
      {hasBounds && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField id={id('min')} label={isText ? 'Min length' : 'Min'} value={field.min} error={problem('min')} onChange={(min) => set({ min })} />
          <NumberField id={id('max')} label={isText ? 'Max length' : 'Max'} value={field.max} error={problem('max')} onChange={(max) => set({ max })} />
        </div>
      )}
      {isText && <TextField id={id('pattern')} label="Pattern" mono value={field.pattern ?? ''} error={problem('pattern')} onChange={(v) => set({ pattern: v || undefined })} />}
      {isCollection && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField id={id('min_items')} label="Min items" value={field.min_items} error={problem('min_items')} onChange={(min_items) => set({ min_items })} />
          <NumberField id={id('max_items')} label="Max items" value={field.max_items} error={problem('max_items')} onChange={(max_items) => set({ max_items })} />
        </div>
      )}
      {field.type === 'list' && (
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('item')}>Item type</Label>
          <select
            id={id('item')}
            className={SELECT}
            value={field.item?.type ?? 'string'}
            onChange={(e) => {
              const t = e.target.value as FieldType;
              set({ item: t === 'enum' ? { name: '', type: t, options: field.item?.options ?? ['option_1'] } : { name: '', type: t } });
            }}
          >
            {SCALAR_TYPES.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </div>
      )}
      {field.type === 'list' && field.item?.type === 'enum' && (
        <OptionsField id={id('item-options')} value={field.item.options ?? []} error={problem('item.options')} onChange={(options) => set({ item: { ...(field.item ?? { name: '', type: 'enum' }), options } })} />
      )}
      {SCALAR_TYPES.includes(field.type) && (
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('default')}>Default</Label>
          <ScalarInput id={id('default')} field={{ ...field, default: undefined, required: false }} value={field.default} onChange={(d) => set({ default: d })} />
          {problem('default') && (
            <span role="alert" className="text-[11px] text-destructive">
              {problem('default')}
            </span>
          )}
        </div>
      )}
      {(field.type === 'repeater' || field.type === 'object') && (
        <div className="flex flex-col gap-2 border-t border-border pt-3">
          <span className="text-[11px] font-semibold">Sub-schema</span>
          <SchemaBuilder nested value={{ fields: field.fields ?? [] }} onChange={(v) => set({ fields: v.fields })} problems={problems} pathPrefix={path} />
        </div>
      )}
      <div className="flex gap-2 border-t border-border pt-3">
        <Button type="button" variant="ghost" disabled={!canMoveUp} onClick={() => onMove(-1)}>
          Move up
        </Button>
        <Button type="button" variant="ghost" disabled={!canMoveDown} onClick={() => onMove(1)}>
          Move down
        </Button>
        <Button type="button" variant="destructive" className="ml-auto" onClick={onRemove}>
          Delete field
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
  const [text, setText] = useState(value.join('\n'));
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>Options (one per line)</Label>
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

function Checkbox({ id, label, checked, onChange }: { id: string; label: string; checked: boolean; onChange: (c: boolean) => void }) {
  return (
    <label htmlFor={id} className="flex items-center gap-2 text-xs">
      <input id={id} type="checkbox" className="h-4 w-4 accent-[var(--px-accent)]" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  );
}

function AssignmentPreview({ schema }: { schema: VarSchema }) {
  const [vars, setVars] = useState<Vars>({});
  return (
    <section aria-label="Assignment form preview" className="flex flex-col gap-3 rounded-[10px] border border-border bg-card p-4">
      <h3 className="text-[13px] font-semibold">Assignment form</h3>
      <p className="text-[11px] text-muted-foreground">What an operator fills in when assigning a host to this group. Group-scoped values stay on the group.</p>
      <SchemaForm fields={membershipFields(schema)} value={vars} onChange={setVars} idPrefix="preview" />
    </section>
  );
}
