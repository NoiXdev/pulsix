import { useEffect, useId, useRef, useState } from 'react';
import { Plus } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { PageTabs } from '../../../ui/page-tabs.tsx';
import { AssignmentPreview } from '../../variables/AssignmentPreview.tsx';
import { fieldPath, hasProblemUnder, moveField, newField, removeField, replaceField } from '../../variables/fieldEditor.ts';
import { moveItem, newKey, removeItem } from '../../variables/keys.ts';
import { editLocked, originOf } from '../../variables/origin.ts';
import type { Problem, SchemaField, VarSchema } from '../../variables/types.ts';
import { FieldDetail } from './FieldDetail.tsx';
import { FieldRow } from './FieldRow.tsx';
import { useT } from '../../../i18n/useT.ts';

type PanelTab = 'field' | 'preview';

export interface FieldsEditorProps {
  schema: VarSchema;
  onChange: (next: VarSchema) => void;
  /** Schema problems from the server (fields[0].name). */
  problems?: readonly Problem[];
  readOnly?: boolean;
}

/**
 * FieldsEditor is the body of the Manage fields dialog: on the left one
 * readable row per field (FieldRow), reordered with up/down, "Add field"
 * below; on the right a panel with two tabs, the selected field's settings
 * (FieldDetail) and the assignment form the schema generates. The first
 * field is selected until another is picked; an added field is selected.
 *
 * Every change of an existing field goes through editLocked, as in the
 * VariableEditor, so a field locked to a source would turn adjusted.
 */
export function FieldsEditor({ schema, onChange, problems, readOnly }: FieldsEditorProps) {
  const t = useT();
  const f = t.inventory.fields;
  const fields = schema.fields;
  // Stable per-field keys, parallel to `fields` (keys.ts). The editor is
  // keyed by the draft's generation, so a wholesale replacement remounts it.
  const [keys, setKeys] = useState<string[]>(() => fields.map(newKey));
  const stable = keys.length === fields.length ? keys : fields.map((_, i) => `i${i}`);
  const [picked, setPicked] = useState<string | null>(null);
  const [tab, setTab] = useState<PanelTab>('field');
  const at = picked === null ? -1 : stable.indexOf(picked);
  const selected = at >= 0 ? at : fields.length > 0 ? 0 : -1;
  const list = useRef<HTMLUListElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const ids = useId();
  // After a move the pressed button's row is re-inserted, which drops its
  // focus; after an add the new field's name takes it.
  const refocus = useRef<{ move?: { key: string; dir: 'up' | 'down' }; name?: boolean } | null>(null);
  useEffect(() => {
    const want = refocus.current;
    if (!want) return;
    refocus.current = null;
    if (want.name) {
      panel.current?.querySelector<HTMLInputElement>('input[id$="-name"]')?.focus();
      return;
    }
    if (!want.move) return;
    const { key, dir } = want.move;
    const button = (d: string) => list.current?.querySelector<HTMLButtonElement>(`[data-move="${key}:${d}"]`);
    const same = button(dir);
    (same && !same.disabled ? same : button(dir === 'up' ? 'down' : 'up'))?.focus();
  });

  const setFields = (next: SchemaField[], nextKeys: string[]) => {
    setKeys(nextKeys);
    onChange({ fields: next });
  };
  const update = (i: number, next: SchemaField) => setFields(replaceField(fields, i, editLocked(fields[i], next)), stable);
  const move = (i: number, dir: -1 | 1) => {
    refocus.current = { move: { key: stable[i], dir: dir < 0 ? 'up' : 'down' } };
    setFields(moveField(fields, i, i + dir), moveItem(stable, i, i + dir));
  };
  const add = () => {
    const key = newKey();
    setFields([...fields, newField(fields)], [...stable, key]);
    setPicked(key);
    setTab('field');
    refocus.current = { name: true };
  };
  const remove = (i: number) => {
    const nextKeys = removeItem(stable, i);
    setFields(removeField(fields, i), nextKeys);
    setPicked(nextKeys[Math.min(i, nextKeys.length - 1)] ?? null);
  };

  const listProblems = (problems ?? []).filter((p) => p.path === '');
  const field = selected >= 0 ? fields[selected] : undefined;
  const removable = field !== undefined && (originOf(field) === 'manual' || originOf(field) === 'gone');
  const tabId = (id: PanelTab) => `${ids}-tab-${id}`;
  const panelId = `${ids}-panel`;

  return (
    <div className="grid min-h-0 gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <section className="flex min-w-0 flex-col gap-2 md:max-h-[62vh] md:overflow-y-auto md:pr-1">
        <span className="text-xs text-muted-foreground">
          {t.inventory.schemaTab.summary(fields.length, fields.filter((x) => x.scope === 'host').length)}
        </span>
        {listProblems.length > 0 && (
          <ul role="alert" className="flex flex-col gap-0.5 text-[11px] text-destructive">
            {listProblems.map((p, i) => (
              <li key={i}>{p.message}</li>
            ))}
          </ul>
        )}
        <ul ref={list} aria-label={f.list} className="flex flex-col gap-1.5">
          {fields.map((x, i) => (
            <FieldRow
              key={stable[i]}
              rowKey={stable[i]}
              field={x}
              selected={i === selected}
              hasProblems={hasProblemUnder(problems, fieldPath('', i))}
              readOnly={readOnly}
              canMoveUp={i > 0}
              canMoveDown={i < fields.length - 1}
              onSelect={() => {
                setPicked(stable[i]);
                setTab('field');
              }}
              onChange={(next) => update(i, next)}
              onMove={(dir) => move(i, dir)}
            />
          ))}
        </ul>
        {fields.length === 0 && <p className="px-1 py-2 text-xs text-muted-foreground">{f.noFields}</p>}
        {!readOnly && (
          <Button type="button" variant="ghost" className="self-start" onClick={add}>
            <Plus size={14} /> {f.addField}
          </Button>
        )}
      </section>
      <div className="flex min-w-0 flex-col gap-3 rounded-[10px] border border-border bg-background/40 p-3 md:max-h-[62vh] md:overflow-y-auto">
        <PageTabs
          label={f.tabs.label}
          tabs={[
            { id: 'field', label: f.tabs.field },
            { id: 'preview', label: f.tabs.preview },
          ]}
          active={tab}
          onSelect={(next) => setTab(next)}
          tabId={tabId}
          panelId={panelId}
        />
        <div ref={panel} role="tabpanel" id={panelId} aria-labelledby={tabId(tab)} className="min-w-0">
          {tab === 'preview' ? (
            <AssignmentPreview schema={schema} />
          ) : field ? (
            <FieldDetail
              key={stable[selected]}
              field={field}
              path={fieldPath('', selected)}
              problems={problems}
              readOnly={readOnly}
              onRemove={removable ? () => remove(selected) : undefined}
              onChange={(next) => update(selected, next)}
            />
          ) : (
            <p className="text-xs text-muted-foreground">{f.selectHint}</p>
          )}
        </div>
      </div>
    </div>
  );
}
