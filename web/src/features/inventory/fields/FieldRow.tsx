import { ArrowDown, ArrowUp } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { Tag } from '../../../ui/badge.tsx';
import { cn } from '../../../lib/utils.ts';
import { changeType, formatValue } from '../../variables/fieldEditor.ts';
import { isActive } from '../../variables/origin.ts';
import type { SchemaField } from '../../variables/types.ts';
import { RequiredBadge, ScopeBadge, TypeBadge } from './FieldBadges.tsx';
import { toggledScope, withRequired } from './fieldEdits.ts';
import { useT } from '../../../i18n/useT.ts';

export interface FieldRowProps {
  field: SchemaField;
  /** Stable identity of the row, for the focus after a move. */
  rowKey: string;
  selected: boolean;
  /** The server reported a problem at or under this field. */
  hasProblems: boolean;
  readOnly?: boolean;
  canMoveUp: boolean;
  canMoveDown: boolean;
  onSelect: () => void;
  onChange: (next: SchemaField) => void;
  onMove: (dir: -1 | 1) => void;
}

/**
 * FieldRow is one field of the Manage fields list, readable at a glance:
 * its name, label and default as text, and three badges — type (a menu),
 * scope and required (each a one-click toggle). The text part is a button
 * that selects the field for the panel beside the list.
 */
export function FieldRow({ field, rowKey, selected, hasProblems, readOnly, canMoveUp, canMoveDown, onSelect, onChange, onMove }: FieldRowProps) {
  const t = useT();
  const f = t.inventory.fields;
  const shownDefault = field.type === 'secret' || field.default === undefined ? f.noDefault : f.defaultIs(formatValue(field.default));
  return (
    <li className={cn('flex items-center gap-2 rounded-md border px-2 py-1.5', selected ? 'border-primary bg-primary/10' : 'border-border bg-card')}>
      {!readOnly && (
        <div className="flex shrink-0 flex-col">
          <Button type="button" variant="ghost" className="h-5 px-1" data-move={`${rowKey}:up`} aria-label={t.variables.builder.moveFieldUp(field.name)} disabled={!canMoveUp} onClick={() => onMove(-1)}>
            <ArrowUp size={12} />
          </Button>
          <Button type="button" variant="ghost" className="h-5 px-1" data-move={`${rowKey}:down`} aria-label={t.variables.builder.moveFieldDown(field.name)} disabled={!canMoveDown} onClick={() => onMove(1)}>
            <ArrowDown size={12} />
          </Button>
        </div>
      )}
      {/* The spaces between the parts keep them apart in the button's
          accessible name; a flex container does not render them. */}
      <button
        type="button"
        aria-pressed={selected}
        onClick={onSelect}
        className="flex min-w-0 flex-1 flex-col items-start rounded px-1 py-0.5 text-left hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
      >
        <span className="flex max-w-full items-center gap-1.5">
          <span className={cn('truncate font-mono text-[13px]', !isActive(field) && 'text-muted-foreground line-through')}>{field.name}</span>{' '}
          {!isActive(field) && <Tag>{f.inactive}</Tag>}{' '}
          {hasProblems && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-destructive" role="img" aria-label={f.hasProblems} />}
        </span>{' '}
        <span className="max-w-full truncate text-[11px] text-muted-foreground">
          {field.label ? `${field.label} · ` : ''}
          {shownDefault}
        </span>
      </button>
      <div className="flex shrink-0 flex-wrap items-center justify-end gap-1">
        <TypeBadge field={field} readOnly={readOnly} onPick={(type) => onChange(changeType(field, type))} />
        <ScopeBadge field={field} readOnly={readOnly} onToggle={() => onChange(toggledScope(field))} />
        <RequiredBadge field={field} readOnly={readOnly} onToggle={() => onChange(withRequired(field, !field.required))} />
      </div>
    </li>
  );
}
