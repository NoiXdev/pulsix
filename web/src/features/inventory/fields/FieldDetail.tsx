import { Trash2 } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { Segmented } from '../../../ui/segmented.tsx';
import { problemAt, scopeOf } from '../../variables/paths.ts';
import { changeType } from '../../variables/fieldEditor.ts';
import { FieldMessages, SwitchField, TextField } from '../../variables/inputs.tsx';
import { isActive } from '../../variables/origin.ts';
import type { FieldScope, Problem, SchemaField } from '../../variables/types.ts';
import { DefaultControl } from './DefaultControl.tsx';
import { FieldMore } from './FieldMore.tsx';
import { TypeCards } from './TypeCards.tsx';
import { withActive, withRequired, withScope, withText } from './fieldEdits.ts';
import { useT } from '../../../i18n/useT.ts';

export interface FieldDetailProps {
  field: SchemaField;
  /** Server path of the field: fields[2]. */
  path: string;
  problems?: readonly Problem[];
  readOnly?: boolean;
  /** Absent when the field may not be removed (read-only, from a source). */
  onRemove?: () => void;
  onChange: (next: SchemaField) => void;
}

/**
 * FieldDetail edits the selected field in the Manage fields panel: name,
 * label, the type as cards, the scope as a segmented control, required and
 * active as switches, the default with the control its type calls for, and
 * below them every other setting the field has (FieldMore). A reader gets
 * all of it disabled.
 */
export function FieldDetail({ field, path, problems, readOnly, onRemove, onChange }: FieldDetailProps) {
  const f = useT().inventory.fields;
  const idBase = `mf-${path.replace(/[^a-zA-Z0-9]/g, '-')}`;
  const id = (k: string) => `${idBase}-${k}`;
  const problem = (k: string) => problemAt(problems, `${path}.${k}`);
  return (
    <fieldset disabled={readOnly} className="flex min-w-0 flex-col gap-4">
      <div className="grid grid-cols-2 gap-3">
        <TextField id={id('name')} label={f.name} mono value={field.name} error={problem('name')} onChange={(name) => onChange({ ...field, name })} />
        <TextField id={id('label')} label={f.label} value={field.label ?? ''} error={problem('label')} onChange={(v) => onChange(withText(field, 'label', v))} />
      </div>
      <TypeCards value={field.type} error={problem('type')} onChange={(type) => onChange(changeType(field, type))} />
      <div className="flex flex-wrap items-start gap-x-8 gap-y-3">
        <div className="flex flex-col gap-1">
          <Segmented<FieldScope>
            label={f.scope}
            options={[
              { value: 'group', label: f.scopeGroup },
              { value: 'host', label: f.scopeHost },
            ]}
            value={scopeOf(field)}
            onChange={(scope) => onChange(withScope(field, scope))}
          />
          <FieldMessages error={problem('scope')} />
        </div>
        <div className="flex flex-col gap-2 pt-5">
          <SwitchField id={id('required')} label={f.requiredSwitch} checked={!!field.required} onChange={(on) => onChange(withRequired(field, on))} />
          <FieldMessages error={problem('required')} />
        </div>
        <div className="flex flex-col gap-1 pt-5">
          <SwitchField id={id('active')} label={f.active} checked={isActive(field)} onChange={(on) => onChange(withActive(field, on))} />
          <span className="max-w-56 text-[11px] text-muted-foreground">{f.activeHint}</span>
        </div>
      </div>
      <DefaultControl field={field} id={id('default')} error={problem('default')} optionsError={problem('options')} onChange={onChange} />
      <FieldMore field={field} path={path} idBase={idBase} problems={problems} readOnly={readOnly} onChange={onChange} />
      {!readOnly && onRemove && (
        <Button type="button" variant="destructive" className="self-start" onClick={onRemove}>
          <Trash2 size={14} /> {f.removeField}
        </Button>
      )}
    </fieldset>
  );
}
