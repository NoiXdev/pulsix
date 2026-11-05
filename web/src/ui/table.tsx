import type { ReactNode } from 'react';
import { cn } from '../lib/utils.ts';

/** Card is the rounded surface="table container" wrapper every list page
 * uses (design's `card()`/table-container shape: surface bg, border,
 * 10px radius).
 *
 * `shrink-0` and `overflow-clip` together keep it at its content height
 * inside AppShell's <main>, a fixed-height flex column that scrolls. A
 * flex item's automatic minimum size is 0 whenever its overflow is not
 * `visible` or `clip`, so the former `overflow-hidden` let the column
 * squeeze a tall card below its rows and clip them (the Backups page and
 * the host page). `overflow-clip` still rounds the corners but is not a
 * scroll container, so it keeps the content-based minimum; `shrink-0` is
 * the belt for any parent that sets its own `min-h-0`. */
export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div data-card="" className={cn('flex shrink-0 flex-col overflow-clip rounded-[10px] border border-border bg-card', className)}>
      {children}
    </div>
  );
}

export function GridRow({
  columns,
  className,
  children,
}: {
  columns: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={cn('grid items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0', className)}
      style={{ gridTemplateColumns: columns }}
    >
      {children}
    </div>
  );
}

export function GridHeader({ columns, children }: { columns: string; children: ReactNode }) {
  return (
    <GridRow
      columns={columns}
      className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase"
    >
      {children}
    </GridRow>
  );
}
