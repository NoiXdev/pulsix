import { ArrowDown, ArrowUp, Check, ChevronDown, ChevronRight } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Switch } from '../../ui/switch.tsx';
import { Tag } from '../../ui/badge.tsx';
import { OriginPill } from './OriginPill.tsx';
import { SubFieldList } from './SubFieldList.tsx';
import { scopeOf } from './paths.ts';
import { formatValue, hasProblemUnder, typeLabel } from './fieldEditor.ts';
import { isActive, originOf } from './origin.ts';
import type { EditorVariant, Problem, SchemaField } from './types.ts';
import { useT } from '../../i18n/useT.ts';

export interface FieldRowProps {
  field: SchemaField;
  /** Stable identity of the row, for focus after a move. */
  rowKey: string;
  /** Server path of the field: fields[2]. */
  path: string;
  variant: EditorVariant;
  problems?: readonly Problem[];
  readOnly?: boolean;
  /** How many cells a row has, for the expanded sub-field row. */
  columns: number;
  canMoveUp: boolean;
  canMoveDown: boolean;
  expanded: boolean;
  onToggle: () => void;
  onOpen: () => void;
  onChange: (next: SchemaField) => void;
  onMove: (dir: -1 | 1) => void;
}

function withActive(f: SchemaField, on: boolean): SchemaField {
  const next = { ...f };
  if (on) delete next.active;
  else next.active = false;
  return next;
}

/**
 * FieldRow is one top-level field in the variable editor's list: its name
 * opens the field dialog, the switch makes it active or inactive, and a
 * repeater or object expands into an inline list of its sub-fields.
 */
export function FieldRow(props: FieldRowProps) {
  const { field, rowKey, path, variant, problems, readOnly, columns, canMoveUp, canMoveDown, expanded, onToggle, onOpen, onChange, onMove } = props;
  const t = useT();
  const hasSubFields = field.type === 'repeater' || field.type === 'object';
  const subListId = `ve-sub-${rowKey}`;
  return (
    <>
      <tr className="border-t border-border align-middle">
        <td className="px-4 py-1.5">
          <div className="flex items-center gap-1">
            {hasSubFields ? (
              <Button type="button" variant="ghost" className="h-7 px-1" aria-expanded={expanded} aria-controls={subListId} aria-label={t.variables.builder.showSubFields(field.name)} onClick={onToggle}>
                {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              </Button>
            ) : (
              <span className="w-[26px]" />
            )}
            {/* The spaces between the parts keep them apart in the button's
                accessible name; a flex container does not render them. */}
            <button type="button" onClick={onOpen} className="flex min-w-0 flex-1 items-center gap-2 rounded px-1 py-1 text-left hover:bg-accent">
              <span className="truncate font-mono text-[13px]">{field.name}</span>{' '}
              {field.label && <span className="truncate text-[11px] text-muted-foreground">{field.label}</span>}{' '}
              {variant === 'group' && scopeOf(field) === 'host' && <Tag>{t.variables.builder.hostTag}</Tag>}{' '}
              {field.set_in_playbook && <Tag>{t.variables.field.setInPlaybook}</Tag>}{' '}
              {hasProblemUnder(problems, path) && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-destructive" aria-label={t.variables.builder.hasProblems} />}
            </button>
          </div>
        </td>
        <td className="px-2 py-1.5 font-mono text-muted-foreground">{typeLabel(field, t)}</td>
        <td className="px-2 py-1.5">
          {field.required && (
            <span role="img" aria-label={t.variables.builder.required} className="inline-flex text-foreground">
              <Check size={14} aria-hidden="true" />
            </span>
          )}
        </td>
        <td className="max-w-[200px] truncate px-2 py-1.5 font-mono text-muted-foreground">{field.type === 'secret' ? '—' : formatValue(field.default)}</td>
        {variant === 'template' && (
          <td className="px-2 py-1.5">
            <OriginPill origin={originOf(field)} />
          </td>
        )}
        <td className="px-2 py-1.5">
          <Switch aria-label={t.variables.builder.activeSwitch(field.name)} checked={isActive(field)} disabled={readOnly} onCheckedChange={(on) => onChange(withActive(field, on))} />
        </td>
        <td className="px-2 py-1.5">
          <div className="flex justify-end gap-1">
            <Button type="button" variant="ghost" className="h-7 px-1.5" data-move={`${rowKey}:up`} aria-label={t.variables.builder.moveFieldUp(field.name)} disabled={readOnly || !canMoveUp} onClick={() => onMove(-1)}>
              <ArrowUp size={14} />
            </Button>
            <Button type="button" variant="ghost" className="h-7 px-1.5" data-move={`${rowKey}:down`} aria-label={t.variables.builder.moveFieldDown(field.name)} disabled={readOnly || !canMoveDown} onClick={() => onMove(1)}>
              <ArrowDown size={14} />
            </Button>
          </div>
        </td>
      </tr>
      {hasSubFields && expanded && (
        <tr id={subListId} className="bg-background/40">
          <td colSpan={columns} className="px-4 pt-1 pb-3 pl-12">
            <SubFieldList
              field={field}
              path={path}
              variant={variant}
              problems={problems}
              locked={originOf(field) === 'source'}
              readOnly={readOnly}
              onChange={(fields) => onChange({ ...field, fields })}
            />
          </td>
        </tr>
      )}
    </>
  );
}
