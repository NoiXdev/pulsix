import type { ReactNode } from 'react';
import { cn } from '../lib/utils.ts';

/** Card is the rounded surface="table container" wrapper every list page
 * uses (design's `card()`/table-container shape: surface bg, border,
 * 10px radius). */
export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn('flex flex-col overflow-hidden rounded-[10px] border border-border bg-card', className)}>
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
