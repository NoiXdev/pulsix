import { ArrowDown, ArrowUp, ArrowUpDown } from 'lucide-react';
import { cn } from '../../lib/utils.ts';
import { nextSort } from './sort.ts';
import type { DataListColumn, SortState } from './types.ts';

/**
 * SortHeader is one column header. A sortable column (a `sortKey` and a
 * change handler) carries `aria-sort` and holds a button that steps the
 * sort; any other is plain text. An icon column's empty label is named for
 * screen readers by its `menuLabel`.
 */
export function SortHeader<Row>({
  column,
  sort,
  onSortChange,
}: {
  column: DataListColumn<Row>;
  sort?: SortState | null;
  onSortChange?: (next: SortState | null) => void;
}) {
  const end = column.align === 'end';
  const key = column.sortKey;
  if (!key || !onSortChange) {
    return (
      <div role="columnheader" className={cn('min-w-0 truncate', end && 'text-right')}>
        {column.label || <span className="sr-only">{column.menuLabel ?? column.id}</span>}
      </div>
    );
  }
  const active = sort?.key === key ? sort.order : null;
  const Arrow = active === 'asc' ? ArrowUp : active === 'desc' ? ArrowDown : ArrowUpDown;
  return (
    <div
      role="columnheader"
      aria-sort={active === 'asc' ? 'ascending' : active === 'desc' ? 'descending' : 'none'}
      className={cn('min-w-0', end && 'text-right')}
    >
      <button
        type="button"
        onClick={() => onSortChange(nextSort(sort, key))}
        className={cn(
          'inline-flex max-w-full items-center gap-1 rounded-sm uppercase hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
          active && 'text-foreground',
        )}
      >
        <span className="truncate">{column.label || <span className="sr-only">{column.menuLabel ?? column.id}</span>}</span>
        <Arrow size={11} aria-hidden="true" className={cn('shrink-0', active ? 'text-brand' : 'opacity-40')} />
      </button>
    </div>
  );
}
