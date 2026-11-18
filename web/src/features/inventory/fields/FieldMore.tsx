import { Label } from '../../../ui/label.tsx';
import { Select } from '../../../ui/Select.tsx';
import { problemAt, scopeOf } from '../../variables/paths.ts';
import { SCALAR_TYPES } from '../../variables/fieldEditor.ts';
import { FieldMessages, NumberField, OptionsField, SwitchField, TextField } from '../../variables/inputs.tsx';
import { SubFieldList } from '../../variables/SubFieldList.tsx';
import { originOf, subLockOf } from '../../variables/origin.ts';
import type { FieldType, Problem, SchemaField } from '../../variables/types.ts';
import { withText } from './fieldEdits.ts';
import { useT } from '../../../i18n/useT.ts';

/** The attributes FieldDetail edits itself; FieldMore lists every other
 * problem at the field so none stays invisible. */
const SHOWN_ABOVE = ['name', 'label', 'type', 'scope', 'required', 'active', 'default', 'options'];

export interface FieldMoreProps {
  field: SchemaField;
  path: string;
  idBase: string;
  problems?: readonly Problem[];
  readOnly?: boolean;
  onChange: (next: SchemaField) => void;
}

/**
 * FieldMore is the rest of a field's settings, below the everyday ones:
 * description, whether hosts may override a group value, bounds, pattern,
 * item counts, a list's item type (and its options), a repeater's or an
 * object's sub-fields — and any problem the server reported at an
 * attribute that has no input here.
 */
export function FieldMore({ field, path, idBase, problems, readOnly, onChange }: FieldMoreProps) {
  const t = useT();
  const b = t.variables.builder;
  const id = (k: string) => `${idBase}-${k}`;
  const problem = (k: string) => problemAt(problems, `${path}.${k}`);
  const set = (patch: Partial<SchemaField>) => onChange({ ...field, ...patch });
  const isText = field.type === 'string';
  const hasBounds = isText || field.type === 'int' || field.type === 'float';
  const isCollection = field.type === 'list' || field.type === 'repeater';
  const isSubSchema = field.type === 'repeater' || field.type === 'object';
  const hasItemOptions = field.type === 'list' && field.item?.type === 'enum';
  const showOverridable = scopeOf(field) === 'group';

  const shown = [...SHOWN_ABOVE, 'description'];
  if (showOverridable) shown.push('overridable');
  if (hasBounds) shown.push('min', 'max');
  if (isText) shown.push('pattern');
  if (isCollection) shown.push('min_items', 'max_items');
  if (field.type === 'list') shown.push('item');
  if (hasItemOptions) shown.push('item.options');
  if (isSubSchema) shown.push('fields');
  // A sub-field's own problems are the sub-field list's to show.
  const otherProblems = (problems ?? []).filter((p) => {
    if (!p.path.startsWith(`${path}.`)) return false;
    const suffix = p.path.slice(path.length + 1);
    return !suffix.startsWith('fields[') && !shown.includes(suffix);
  });

  return (
    <section aria-label={t.inventory.fields.more} className="flex flex-col gap-3 border-t border-border pt-3">
      <h4 className="text-[11px] font-semibold">{t.inventory.fields.more}</h4>
      <TextField id={id('description')} label={b.description} value={field.description ?? ''} error={problem('description')} onChange={(v) => onChange(withText(field, 'description', v))} />
      {showOverridable && (
        <div className="flex flex-col gap-1">
          <SwitchField id={id('overridable')} label={b.hostsMayOverride} checked={!!field.overridable} onChange={(c) => set({ overridable: c || undefined })} />
          <FieldMessages error={problem('overridable')} />
        </div>
      )}
      {hasBounds && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField id={id('min')} label={isText ? b.minLength : b.min} value={field.min} error={problem('min')} onChange={(min) => set({ min })} />
          <NumberField id={id('max')} label={isText ? b.maxLength : b.max} value={field.max} error={problem('max')} onChange={(max) => set({ max })} />
        </div>
      )}
      {isText && <TextField id={id('pattern')} label={b.pattern} mono value={field.pattern ?? ''} error={problem('pattern')} onChange={(v) => onChange(withText(field, 'pattern', v))} />}
      {isCollection && (
        <div className="grid grid-cols-2 gap-3">
          <NumberField id={id('min_items')} label={b.minItems} value={field.min_items} error={problem('min_items')} onChange={(min_items) => set({ min_items })} />
          <NumberField id={id('max_items')} label={b.maxItems} value={field.max_items} error={problem('max_items')} onChange={(max_items) => set({ max_items })} />
        </div>
      )}
      {field.type === 'list' && (
        <div className="flex flex-col gap-1">
          <Label htmlFor={id('item')}>{b.itemType}</Label>
          <Select
            id={id('item')}
            disabled={readOnly}
            options={SCALAR_TYPES.map((ty) => ({ value: ty, label: ty }))}
            value={field.item?.type ?? 'string'}
            onChange={(v) => {
              const ty = v as FieldType;
              // A default of the old item type would no longer fit.
              const next = { ...field, item: ty === 'enum' ? { name: '', type: ty, options: field.item?.options ?? ['option_1'] } : { name: '', type: ty } };
              delete next.default;
              onChange(next);
            }}
          />
          <FieldMessages error={problem('item')} />
        </div>
      )}
      {hasItemOptions && field.item && (
        <OptionsField id={id('item-options')} value={field.item.options ?? []} error={problem('item.options')} onChange={(options) => set({ item: { ...(field.item ?? { name: '', type: 'enum' }), options } })} />
      )}
      {isSubSchema && (
        <div className="flex flex-col gap-2">
          <span className="text-[11px] font-semibold">{b.subSchemaHeading}</span>
          <SubFieldList
            field={field}
            path={path}
            variant="group"
            problems={problems}
            locked={originOf(field) === 'source'}
            lock={subLockOf(field)}
            readOnly={readOnly}
            onChange={(fields) => set({ fields })}
          />
        </div>
      )}
      {otherProblems.length > 0 && (
        <ul role="alert" className="flex flex-col gap-0.5 text-[11px] text-destructive">
          {otherProblems.map((p, i) => (
            <li key={i}>
              <span className="font-mono">{p.path.slice(path.length + 1)}</span> · {p.message}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
