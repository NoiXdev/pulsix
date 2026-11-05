import { useRef, useState } from 'react';
import { ArrowDown, ArrowUp, Pencil, Plus, Trash2 } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { problemAt } from './paths.ts';
import { changeType, fieldPath, hasProblemUnder, moveField, newField, removeField, replaceField, typesFor } from './fieldEditor.ts';
import { FieldMessages, SwitchField, TextField } from './inputs.tsx';
import { FieldDialog } from './FieldDialog.tsx';
import { moveItem, newKey, removeItem } from './keys.ts';
import { useFocusReturn } from './focusReturn.ts';
import { subLockBelow, subNameLocked, type SubLock } from './origin.ts';
import type { EditorVariant, FieldType, Problem, SchemaField } from './types.ts';
import { useT } from '../../i18n/useT.ts';

export interface SubFieldListProps {
  /** The repeater or object whose sub-fields these are. */
  field: SchemaField;
  /** Server path of that field. */
  path: string;
  variant: EditorVariant;
  problems?: readonly Problem[];
  /** The top-level field is locked to its source: no new sub-fields. */
  locked: boolean;
  /** Which sub-field names the source fixes (origin.ts); none by default. */
  lock?: SubLock;
  readOnly?: boolean;
  onChange: (fields: SchemaField[]) => void;
}

/**
 * SubFieldList edits the sub-fields of a repeater or object inline: name,
 * type and required in the row, everything else in the sub-field's own
 * dialog (which edits a nested repeater's sub-fields with another list).
 */
export function SubFieldList({ field, path, variant, problems, locked, lock = null, readOnly, onChange }: SubFieldListProps) {
  const t = useT();
  const fields = field.fields ?? [];
  // Stable keys move with their sub-field on add/move/remove, so a row's
  // inputs (and their focus) follow the sub-field, not its index. A length
  // change from outside (a reset) falls back to index keys.
  const [keys, setKeys] = useState<string[]>(() => fields.map(newKey));
  const stable = keys.length === fields.length ? keys : fields.map((_, i) => `i${i}`);
  const [editing, setEditing] = useState<number | null>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const rememberOpener = useFocusReturn(editing !== null, () => addButton.current);
  const set = (next: SchemaField[], nextKeys: string[]) => {
    setKeys(nextKeys);
    onChange(next);
  };
  const idBase = `ve-${path.replace(/[^a-zA-Z0-9]/g, '-')}`;
  const listProblems = (problems ?? []).filter((p) => p.path === path || p.path === `${path}.fields`);

  return (
    <div className="flex flex-col gap-2">
      {listProblems.length > 0 && (
        <ul role="alert" className="flex flex-col gap-0.5 text-[11px] text-destructive">
          {listProblems.map((p, i) => (
            <li key={i}>{p.message}</li>
          ))}
        </ul>
      )}
      {fields.map((f, i) => {
        const sub = fieldPath(path, i);
        const id = (k: string) => `${idBase}-${stable[i]}-${k}`;
        return (
          <div
            key={stable[i]}
            role="group"
            aria-label={t.variables.builder.subFieldGroup(f.name)}
            className="grid grid-cols-[minmax(0,1fr)_180px_auto_auto] items-start gap-3 rounded-md border border-border bg-background px-3 py-2"
          >
            <TextField id={id('name')} label={t.variables.builder.name} mono readOnly={readOnly || subNameLocked(lock, f)} value={f.name} error={problemAt(problems, `${sub}.name`)} onChange={(name) => set(replaceField(fields, i, { ...f, name }), stable)} />
            <div className="flex flex-col gap-1">
              <Label htmlFor={id('type')}>{t.variables.builder.type}</Label>
              <Select
                id={id('type')}
                disabled={readOnly}
                options={typesFor(variant).map((ty) => ({ value: ty, label: ty === 'object' ? t.variables.builder.subSchemaObject : ty }))}
                value={f.type}
                onChange={(v) => set(replaceField(fields, i, changeType(f, v as FieldType)), stable)}
              />
              <FieldMessages error={problemAt(problems, `${sub}.type`)} />
            </div>
            <div className="pt-5">
              <SwitchField id={id('required')} label={t.variables.builder.requiredLabel} disabled={readOnly} checked={!!f.required} onChange={(c) => set(replaceField(fields, i, { ...f, required: c || undefined }), stable)} />
            </div>
            <div className="flex items-center gap-1 pt-4">
              {hasProblemUnder(problems, sub) && <span className="h-1.5 w-1.5 rounded-full bg-destructive" aria-label={t.variables.builder.hasProblems} />}
              <Button type="button" variant="ghost" aria-label={t.variables.builder.editSubField(f.name)} onClick={(e) => {
                  rememberOpener(e.currentTarget);
                  setEditing(i);
                }}>
                <Pencil size={14} />
              </Button>
              <Button type="button" variant="ghost" aria-label={t.variables.builder.moveFieldUp(f.name)} disabled={readOnly || i === 0} onClick={() => set(moveField(fields, i, i - 1), moveItem(stable, i, i - 1))}>
                <ArrowUp size={14} />
              </Button>
              <Button type="button" variant="ghost" aria-label={t.variables.builder.moveFieldDown(f.name)} disabled={readOnly || i === fields.length - 1} onClick={() => set(moveField(fields, i, i + 1), moveItem(stable, i, i + 1))}>
                <ArrowDown size={14} />
              </Button>
              <Button type="button" variant="ghost" aria-label={t.variables.builder.removeSubField(f.name)} disabled={readOnly} onClick={() => set(removeField(fields, i), removeItem(stable, i))}>
                <Trash2 size={14} />
              </Button>
            </div>
          </div>
        );
      })}
      {!readOnly && (
        <Button ref={addButton} type="button" variant="ghost" className="self-start" disabled={locked} onClick={() => set([...fields, newField(fields)], [...stable, newKey()])}>
          <Plus size={14} /> {t.variables.builder.addSubField}
        </Button>
      )}
      {editing !== null && editing < fields.length && (
        <FieldDialog
          field={fields[editing]}
          path={fieldPath(path, editing)}
          variant={variant}
          nested
          subFieldsLocked={locked}
          nameLocked={subNameLocked(lock, fields[editing])}
          subLock={subLockBelow(lock, fields[editing])}
          problems={problems}
          readOnly={readOnly}
          onSave={(next) => {
            set(replaceField(fields, editing, next), stable);
            setEditing(null);
          }}
          onRemove={() => {
            set(removeField(fields, editing), removeItem(stable, editing));
            setEditing(null);
          }}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  );
}
