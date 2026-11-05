import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { ScalarInput } from './SchemaForm.tsx';
import { problemAt, scopeOf } from './paths.ts';
import { SCALAR_TYPES, changeType, typesFor } from './fieldEditor.ts';
import { FieldMessages, NumberField, OptionsField, SwitchField, TextField } from './inputs.tsx';
import { SubFieldList } from './SubFieldList.tsx';
import type { EditorVariant, FieldType, Problem, SchemaField } from './types.ts';
import { useT } from '../../i18n/useT.ts';

export interface FieldAttributesProps {
  field: SchemaField;
  /** Server path of the field: fields[2], or fields[2].fields[0]. */
  path: string;
  idBase: string;
  /** A sub-field: no scope, no host override, and its own sub-fields are
   * edited here (a top-level field's are edited in its row). */
  nested: boolean;
  variant: EditorVariant;
  problems?: readonly Problem[];
  /** The name of a field from the playbook is the playbook's. */
  nameLocked: boolean;
  /** No new sub-fields: the top-level field is locked to its source. */
  subFieldsLocked: boolean;
  /** "Source says: …" per attribute that differs from the source. */
  says: Readonly<Record<string, string>>;
  onChange: (f: SchemaField) => void;
}

/**
 * FieldAttributes edits every attribute of one field, each problem from the
 * server and each difference from the source shown at the attribute it is
 * about; whatever has no input of its own is listed at the end, so nothing
 * stays invisible.
 */
export function FieldAttributes({ field, path, idBase, nested, variant, problems, nameLocked, subFieldsLocked, says, onChange }: FieldAttributesProps) {
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
  const showScope = !nested && variant === 'group';
  const showOverridable = showScope && scopeOf(field) === 'group';

  // Every attribute this editor gives its own slot to, so the catch-all
  // lists below never repeat what is already shown next to its input.
  const shown = ['name', 'type', 'label', 'description', 'required'];
  if (showScope) shown.push('scope');
  if (showOverridable) shown.push('overridable');
  if (field.type === 'enum') shown.push('options');
  if (hasBounds) shown.push('min', 'max');
  if (isText) shown.push('pattern');
  if (isCollection) shown.push('min_items', 'max_items');
  if (field.type === 'list') shown.push('item');
  if (hasItemOptions) shown.push('item.options');
  if (hasDefault) shown.push('default');
  if (isSubSchema) shown.push('fields');

  // Every other problem at or under this field's own path. An exact match
  // on `path`, and `fields`, belong to the sub-field list; anything under
  // `fields[` to a sub-field, shown by that sub-field's own editor.
  const otherProblems = (problems ?? []).filter((p) => {
    if (!p.path.startsWith(`${path}.`)) return false;
    const suffix = p.path.slice(path.length + 1);
    if (suffix.startsWith('fields[')) return false;
    return !shown.includes(suffix);
  });
  const otherSays = Object.entries(says).filter(([k]) => !shown.includes(k) || (k === 'fields' && !nested));

  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-2 gap-3">
        <TextField id={id('name')} label={t.variables.builder.name} mono readOnly={nameLocked} value={field.name} error={problem('name')} onChange={(v) => set({ name: v })} />
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('type')}>{t.variables.builder.type}</Label>
          <Select
            id={id('type')}
            options={typesFor(variant).map((ty) => ({ value: ty, label: ty === 'object' ? t.variables.builder.subSchemaObject : ty }))}
            value={field.type}
            onChange={(v) => onChange(changeType(field, v as FieldType))}
          />
          <FieldMessages error={problem('type')} note={says.type} />
        </div>
      </div>
      <TextField id={id('label')} label={t.variables.builder.label} value={field.label ?? ''} note={says.label} onChange={(v) => set({ label: v || undefined })} />
      <TextField id={id('description')} label={t.variables.builder.description} value={field.description ?? ''} note={says.description} onChange={(v) => set({ description: v || undefined })} />

      {showScope && (
        <fieldset className="flex flex-col gap-1">
          <legend className="text-[11px] font-medium text-muted-foreground">{t.variables.builder.scope}</legend>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name={id('scope')} checked={scopeOf(field) === 'group'} onChange={() => set({ scope: undefined })} />
            {t.variables.builder.scopeGroup}
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name={id('scope')} checked={scopeOf(field) === 'host'} onChange={() => set({ scope: 'host', overridable: undefined })} />
            {t.variables.builder.scopeHost}
          </label>
          <FieldMessages error={problem('scope')} note={says.scope} />
        </fieldset>
      )}
      <SwitchField id={id('required')} label={t.variables.builder.requiredLabel} checked={!!field.required} note={says.required} onChange={(c) => set({ required: c || undefined })} />
      {showOverridable && (
        <div className="flex flex-col gap-1">
          <SwitchField id={id('overridable')} label={t.variables.builder.hostsMayOverride} checked={!!field.overridable} note={says.overridable} onChange={(c) => set({ overridable: c || undefined })} />
          <FieldMessages error={problem('overridable')} />
        </div>
      )}

      {field.type === 'enum' && (
        <OptionsField id={id('options')} value={field.options ?? []} error={problem('options')} note={says.options} onChange={(options) => set({ options, default: undefined })} />
      )}
      {hasBounds && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField id={id('min')} label={isText ? t.variables.builder.minLength : t.variables.builder.min} value={field.min} error={problem('min')} note={says.min} onChange={(min) => set({ min })} />
          <NumberField id={id('max')} label={isText ? t.variables.builder.maxLength : t.variables.builder.max} value={field.max} error={problem('max')} note={says.max} onChange={(max) => set({ max })} />
        </div>
      )}
      {isText && <TextField id={id('pattern')} label={t.variables.builder.pattern} mono value={field.pattern ?? ''} error={problem('pattern')} note={says.pattern} onChange={(v) => set({ pattern: v || undefined })} />}
      {isCollection && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField id={id('min_items')} label={t.variables.builder.minItems} value={field.min_items} error={problem('min_items')} note={says.min_items} onChange={(min_items) => set({ min_items })} />
          <NumberField id={id('max_items')} label={t.variables.builder.maxItems} value={field.max_items} error={problem('max_items')} note={says.max_items} onChange={(max_items) => set({ max_items })} />
        </div>
      )}
      {field.type === 'list' && (
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('item')}>{t.variables.builder.itemType}</Label>
          <Select
            id={id('item')}
            options={SCALAR_TYPES.map((ty) => ({ value: ty, label: ty }))}
            value={field.item?.type ?? 'string'}
            onChange={(v) => {
              const ty = v as FieldType;
              set({ item: ty === 'enum' ? { name: '', type: ty, options: field.item?.options ?? ['option_1'] } : { name: '', type: ty } });
            }}
          />
          <FieldMessages note={says.item} />
        </div>
      )}
      {hasItemOptions && field.item && (
        <OptionsField id={id('item-options')} value={field.item.options ?? []} error={problem('item.options')} onChange={(options) => set({ item: { ...(field.item ?? { name: '', type: 'enum' }), options } })} />
      )}
      {hasDefault && (
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('default')}>{t.variables.builder.default}</Label>
          <ScalarInput id={id('default')} field={{ ...field, default: undefined, required: false }} value={field.default} onChange={(d) => set({ default: d })} />
          <FieldMessages error={problem('default')} note={says.default} />
        </div>
      )}
      {isSubSchema && nested && (
        <div className="flex flex-col gap-2 border-t border-border pt-3">
          <span className="text-[11px] font-semibold">{t.variables.builder.subSchemaHeading}</span>
          <SubFieldList
            field={field}
            path={path}
            variant={variant}
            problems={problems}
            locked={subFieldsLocked}
            onChange={(fields) => set({ fields })}
          />
        </div>
      )}
      {otherSays.length > 0 && (
        <ul className="flex flex-col gap-0.5 text-[11px] text-muted-foreground">
          {otherSays.map(([k, v]) => (
            <li key={k}>
              <span className="font-mono">{k}</span> · {v}
            </li>
          ))}
        </ul>
      )}
      {otherProblems.length > 0 && (
        <ul role="alert" className="flex flex-col gap-0.5 text-[11px] text-destructive">
          {otherProblems.map((p, i) => (
            <li key={i}>{p.message}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
