import { useEffect, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { Plus } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { FieldRow } from './FieldRow.tsx';
import { FieldDialog } from './FieldDialog.tsx';
import { AssignmentPreview } from './AssignmentPreview.tsx';
import { fieldPath, moveField, newField, removeField, replaceField } from './fieldEditor.ts';
import { editLocked } from './origin.ts';
import { useFocusReturn } from './focusReturn.ts';
import { moveItem, newKey, removeItem } from './keys.ts';
import type { FieldUsage } from './usage.ts';
import type { EditorVariant, Problem, SchemaField, VarSchema } from './types.ts';
import { useT } from '../../i18n/useT.ts';

export interface VariableEditorProps {
  value: VarSchema;
  onChange: (next: VarSchema) => void;
  /** Schema problems from the server (fields[0].fields[2].name). */
  problems?: readonly Problem[];
  variant: EditorVariant;
  /** Where each template variable is overridden (shown in its dialog). */
  usage?: FieldUsage;
  /** Where the entries of a field's "Used in" lead. */
  usageLinks?: { org: string; onOpenWebhooks: () => void };
  readOnly?: boolean;
}

/** null: no dialog; 'new': a field not in the schema yet; else its row key. */
type Editing = null | 'new' | { key: string };

/**
 * VariableEditor edits a variable schema (spec 7.75 goal 1) for an
 * inventory group, a job template or a shared template definition: a
 * full-width list of the top-level fields, one field at a time in a dialog,
 * the sub-fields of a repeater or object inline in its expanded row, and
 * for a group the generated assignment form as a live preview below.
 *
 * Every change of an existing field goes through editLocked, so a field
 * locked to the playbook turns adjusted whatever part of it was edited
 * (dialog, sub-field, type) and only the active switch leaves it locked.
 */
export function VariableEditor({ value, onChange, problems, variant, usage, usageLinks, readOnly }: VariableEditorProps) {
  const t = useT();
  const fields = value.fields;
  // Stable per-field keys, parallel to `fields` (keys.ts). A length change
  // from outside (the parent resetting the draft) falls back to index keys
  // for this render; the next add/move/remove resyncs.
  const [keys, setKeys] = useState<string[]>(() => fields.map(newKey));
  const stable = keys.length === fields.length ? keys : fields.map((_, i) => `i${i}`);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const [editing, setEditing] = useState<Editing>(null);
  const table = useRef<HTMLTableElement>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  const rememberOpener = useFocusReturn(editing !== null, () => addButton.current);
  // A moved row's <tr> is re-inserted into the table, which drops the focus
  // of the move button just pressed; it is put back after the render.
  const refocus = useRef<{ key: string; dir: 'up' | 'down' } | null>(null);
  useEffect(() => {
    const want = refocus.current;
    if (!want) return;
    refocus.current = null;
    const button = (dir: string) => table.current?.querySelector<HTMLButtonElement>(`[data-move="${want.key}:${dir}"]`);
    const same = button(want.dir);
    const other = button(want.dir === 'up' ? 'down' : 'up');
    (same && !same.disabled ? same : other)?.focus();
  });

  const setFields = (next: SchemaField[], nextKeys: string[]) => {
    setKeys(nextKeys);
    onChange({ fields: next });
  };
  const update = (i: number, next: SchemaField) => setFields(replaceField(fields, i, editLocked(fields[i], next)), stable);
  const remove = (i: number) => setFields(removeField(fields, i), removeItem(stable, i));
  const move = (i: number, dir: -1 | 1) => {
    refocus.current = { key: stable[i], dir: dir < 0 ? 'up' : 'down' };
    setFields(moveField(fields, i, i + dir), moveItem(stable, i, i + dir));
  };
  const toggle = (key: string) =>
    setExpanded((cur) => {
      const next = new Set(cur);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  // The webhooks open in a dialog of the page: the field dialog closes
  // first (its unsaved draft dropped, as Cancel would), committed before the
  // other opens, so the two never stack and the focus returned to the field
  // row cannot land after the webhooks dialog took it.
  const links = usageLinks && {
    ...usageLinks,
    onOpenWebhooks: () => {
      flushSync(() => setEditing(null));
      usageLinks.onOpenWebhooks();
    },
  };
  const columns = variant === 'template' ? 7 : 6;
  const listProblems = (problems ?? []).filter((p) => p.path === '');
  const editIndex = editing && editing !== 'new' ? stable.indexOf(editing.key) : -1;

  return (
    <div className="flex flex-col gap-4">
      {listProblems.length > 0 && (
        <ul role="alert" className="flex flex-col gap-0.5 text-[11px] text-destructive">
          {listProblems.map((p, i) => (
            <li key={i}>{p.message}</li>
          ))}
        </ul>
      )}
      {!readOnly && (
        <Button
          ref={addButton}
          type="button"
          className="self-start"
          onClick={(e) => {
            rememberOpener(e.currentTarget);
            setEditing('new');
          }}
        >
          <Plus size={14} /> {t.variables.builder.addField}
        </Button>
      )}
      {/* Not a Card: its overflow-clip cut off the type list of a sub-field
          opened near the list's bottom (the inline sub-field rows hold
          selects whose options open below them). It still carries
          data-card, so the E2E layout check (expectNoClippedCards) catches
          a field list wider than its column. */}
      <div data-card="" className="flex shrink-0 flex-col rounded-[10px] border border-border bg-card">
        <table ref={table} className="w-full text-xs" aria-label={t.variables.builder.fields}>
          <thead>
            <tr className="text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              <th className="px-4 py-2">{t.variables.builder.columns.name}</th>
              <th className="px-2 py-2">{t.variables.builder.columns.type}</th>
              <th className="px-2 py-2">{t.variables.builder.columns.required}</th>
              <th className="px-2 py-2">{t.variables.builder.columns.default}</th>
              {variant === 'template' && <th className="px-2 py-2">{t.variables.builder.columns.origin}</th>}
              <th className="px-2 py-2">{t.variables.builder.columns.active}</th>
              <th className="px-2 py-2">
                <span className="sr-only">{t.variables.builder.columns.order}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {fields.map((f, i) => (
              <FieldRow
                key={stable[i]}
                rowKey={stable[i]}
                field={f}
                path={fieldPath('', i)}
                variant={variant}
                problems={problems}
                readOnly={readOnly}
                columns={columns}
                canMoveUp={i > 0}
                canMoveDown={i < fields.length - 1}
                expanded={expanded.has(stable[i])}
                onToggle={() => toggle(stable[i])}
                onOpen={(opener) => {
                  rememberOpener(opener);
                  setEditing({ key: stable[i] });
                }}
                onChange={(next) => update(i, next)}
                onMove={(dir) => move(i, dir)}
              />
            ))}
            {fields.length === 0 && (
              <tr className="border-t border-border">
                <td colSpan={columns} className="px-4 py-3 text-muted-foreground">
                  {t.variables.builder.noFields}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {variant === 'group' && <AssignmentPreview schema={value} />}

      {editing === 'new' && (
        <FieldDialog
          field={newField(fields)}
          path={fieldPath('', fields.length)}
          variant={variant}
          onSave={(f) => {
            setFields([...fields, f], [...stable, newKey()]);
            setEditing(null);
          }}
          onClose={() => setEditing(null)}
        />
      )}
      {editIndex >= 0 && (
        <FieldDialog
          field={fields[editIndex]}
          path={fieldPath('', editIndex)}
          variant={variant}
          problems={problems}
          usage={usage ? (Object.hasOwn(usage, fields[editIndex].name) ? usage[fields[editIndex].name] : []) : undefined}
          usageLinks={links}
          readOnly={readOnly}
          subFieldsLocked={fields[editIndex].origin === 'source'}
          onSave={(f) => {
            update(editIndex, f);
            setEditing(null);
          }}
          onRemove={() => {
            remove(editIndex);
            setEditing(null);
          }}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  );
}
