import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useT } from '../../i18n/useT.ts';
import { cn } from '../../lib/utils.ts';
import { Select } from '../Select.tsx';
import { pageItems } from './pageItems.ts';
import type { DataListPaging } from './types.ts';

/** The page sizes a list offers (one setting for all lists). */
const PAGE_SIZES = [10, 25, 50, 100] as const;

export type PagerProps = DataListPaging;

const PAGE_BUTTON =
  'inline-flex h-[26px] min-w-7 items-center justify-center rounded-md border border-border px-1.5 font-mono text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:pointer-events-none disabled:opacity-35';

/**
 * Pager is a list's footer: "1–25 of 137 hosts", the page size and the
 * page buttons (previous, the numbers from pageItems with gaps, next).
 */
export function Pager({ page, perPage, total, countLabel, onPageChange, onPerPageChange }: PagerProps) {
  const t = useT();
  const pageCount = Math.max(1, Math.ceil(total / perPage));
  const from = total === 0 ? 0 : (page - 1) * perPage + 1;
  const to = Math.min(page * perPage, total);
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-border px-4 py-2 text-xs text-muted-foreground">
      <span className="font-mono tabular-nums">{t.ui.dataList.range(from, to, countLabel(total))}</span>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <span aria-hidden="true">{t.ui.dataList.perPage}</span>
          <span className="w-[72px]">
            <Select
              portal
              aria-label={t.ui.dataList.perPage}
              options={PAGE_SIZES.map((n) => ({ value: String(n), label: String(n) }))}
              value={String(perPage)}
              onChange={(v) => onPerPageChange(Number(v))}
            />
          </span>
        </div>
        <nav aria-label={t.ui.dataList.pages} className="flex items-center gap-1">
          <button type="button" className={PAGE_BUTTON} aria-label={t.ui.dataList.previousPage} disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
            <ChevronLeft size={14} aria-hidden="true" />
          </button>
          {pageItems(page, pageCount).map((item, i) =>
            item === 'gap' ? (
              <span key={`gap-${i}`} aria-hidden="true" className="px-1 font-mono">
                …
              </span>
            ) : (
              <button
                key={item}
                type="button"
                aria-label={t.ui.dataList.page(item)}
                aria-current={item === page ? 'page' : undefined}
                onClick={() => onPageChange(item)}
                className={cn(PAGE_BUTTON, item === page && 'border-brand-dim text-brand')}
              >
                {item}
              </button>
            ),
          )}
          <button type="button" className={PAGE_BUTTON} aria-label={t.ui.dataList.nextPage} disabled={page >= pageCount} onClick={() => onPageChange(page + 1)}>
            <ChevronRight size={14} aria-hidden="true" />
          </button>
        </nav>
      </div>
    </div>
  );
}
