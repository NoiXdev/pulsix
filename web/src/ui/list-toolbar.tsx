import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { Search, X } from 'lucide-react';
import { useT } from '../i18n/useT.ts';
import { cn } from '../lib/utils.ts';
import { RowMenuItem } from './list-row.tsx';
import { MenuPopover } from './menu-popover.tsx';

/**
 * The pieces of a list's filter bar, one look for every list: a search box,
 * quick toggle chips, a "+ Filter" menu for the less common filters, the
 * active filters as removable pills, Reset and the result count. They are
 * presentational: where the filters live (the URL, features/lists/
 * filterSearch.ts) is the page's business. ListToolbar goes into
 * DataList's `toolbar` slot.
 */

const CONTROL = 'flex h-[30px] items-center rounded-md border px-3 text-xs font-medium';

/** The filter bar's row: its children wrap; `count` sits at the right. */
export function ListToolbar({ children, count }: { children?: ReactNode; count?: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
      {children}
      {count !== undefined && count !== null && <div className="ml-auto pl-2">{count}</div>}
    </div>
  );
}

export interface SearchInputProps {
  /** The search as the list holds it (the URL's). A change from outside
   * (Back, a link) shows in the box; the echo of its own writes does not. */
  value: string;
  /** Called `debounceMs` after the last keystroke, and at once on Clear. */
  onChange: (value: string) => void;
  /** The box's accessible name. */
  label: string;
  placeholder?: string;
  debounceMs?: number;
  className?: string;
}

/**
 * SearchInput is a list's search box: labelled, with a search icon and a
 * clear button while it holds text. It keeps its own text so typing never
 * waits for the list, and reports it debounced. It follows `value` only for
 * changes it did not write itself: the values it sent and has not yet seen
 * come back are skipped, so a late echo ("a" while the box holds "ab")
 * never takes characters away.
 */
export function SearchInput({ value, onChange, label, placeholder, debounceMs = 250, className }: SearchInputProps) {
  const t = useT();
  const inputRef = useRef<HTMLInputElement>(null);
  const [text, setText] = useState(value);
  const [seen, setSeen] = useState(value);
  // Sent and not yet come back as `value`, oldest first.
  const [sent, setSent] = useState<string[]>([]);
  const [outside, setOutside] = useState(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const onChangeRef = useRef(onChange);
  useLayoutEffect(() => {
    onChangeRef.current = onChange;
  });

  if (value !== seen) {
    setSeen(value);
    const at = sent.indexOf(value);
    if (at >= 0) {
      setSent(sent.slice(at + 1));
    } else {
      setSent([]);
      setText(value);
      setOutside((n) => n + 1);
    }
  }

  // A change from outside wins over a keystroke still waiting to be sent.
  useEffect(() => {
    if (outside > 0) clearTimeout(timer.current);
  }, [outside]);
  useEffect(() => () => clearTimeout(timer.current), []);

  const emit = useCallback((next: string) => {
    clearTimeout(timer.current);
    timer.current = undefined;
    setSent((s) => [...s, next]);
    onChangeRef.current(next);
  }, []);

  return (
    <div
      className={cn(
        'flex h-[30px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-xs text-muted-foreground focus-within:border-primary',
        className,
      )}
    >
      <Search size={13} aria-hidden="true" />
      <input
        ref={inputRef}
        type="search"
        aria-label={label}
        placeholder={placeholder}
        value={text}
        onChange={(e) => {
          const next = e.target.value;
          setText(next);
          clearTimeout(timer.current);
          timer.current = setTimeout(() => emit(next), debounceMs);
        }}
        className="w-40 bg-transparent text-foreground outline-none placeholder:text-muted-foreground [&::-webkit-search-cancel-button]:hidden"
      />
      {text !== '' && (
        <button
          type="button"
          aria-label={t.ui.listToolbar.clearSearch}
          onClick={() => {
            setText('');
            emit('');
            inputRef.current?.focus();
          }}
          className="rounded text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-primary focus-visible:outline-none"
        >
          <X size={13} />
        </button>
      )}
    </div>
  );
}

export type FilterChipTone = 'brand' | 'ok' | 'warn' | 'crit';

const PRESSED: Record<FilterChipTone, string> = {
  brand: 'border-brand/40 bg-brand/10 text-brand',
  ok: 'border-ok/40 bg-ok/10 text-ok',
  warn: 'border-warn/40 bg-warn/10 text-warn',
  crit: 'border-crit/40 bg-crit/10 text-crit',
};

/** A quick filter that is on or off (`aria-pressed`). `tone` colours it
 * while pressed (a "Critical" chip in the critical colour). */
export function FilterChip({
  pressed,
  onToggle,
  children,
  tone = 'brand',
}: {
  pressed: boolean;
  onToggle: () => void;
  children: ReactNode;
  tone?: FilterChipTone;
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onToggle}
      className={cn(
        CONTROL,
        'focus-visible:ring-2 focus-visible:ring-primary focus-visible:outline-none',
        pressed ? PRESSED[tone] : 'border-border bg-card text-muted-foreground hover:text-foreground',
      )}
    >
      {children}
    </button>
  );
}

export interface FilterMenuItem {
  id: string;
  label: string;
  /** Shown dimmed after the label, e.g. the values to choose from. */
  hint?: string;
  onSelect: () => void;
}

/**
 * FilterMenu is the "+ Filter" button and its menu of the less common
 * filters (MenuPopover: arrow keys, Escape, focus back to the button). The
 * page leaves out an item whose filter is already set; with no item left
 * the button is not shown.
 */
export function FilterMenu({ label, items }: { label?: string; items: FilterMenuItem[] }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuId = useId();
  const close = useCallback((restoreFocus: boolean) => {
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus();
  }, []);
  if (items.length === 0) return null;
  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => (open ? close(false) : setOpen(true))}
        className={cn(
          CONTROL,
          'border-dashed border-border bg-card text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-primary focus-visible:outline-none',
        )}
      >
        {label ?? t.ui.listToolbar.addFilter}
      </button>
      {open && (
        <MenuPopover id={menuId} label={t.ui.listToolbar.moreFilters} anchor={triggerRef} onClose={close}>
          {items.map((item) => (
            <RowMenuItem key={item.id} onSelect={item.onSelect}>
              <span className="flex items-baseline justify-between gap-4">
                <span>{item.label}</span>
                {item.hint && <span className="text-[11px] text-muted-foreground">{item.hint}</span>}
              </span>
            </RowMenuItem>
          ))}
        </MenuPopover>
      )}
    </>
  );
}

/** A filter that is set, as a pill with a remove button ("Zone: eu ✕").
 * `removeLabel` names the button; it defaults to "Remove filter <label>". */
export function ActiveFilter({ label, onRemove, removeLabel }: { label: string; onRemove: () => void; removeLabel?: string }) {
  const t = useT();
  return (
    <span className="inline-flex h-[26px] items-center gap-1.5 rounded-full border border-border bg-card pr-1.5 pl-3 text-xs text-foreground">
      {label}
      <button
        type="button"
        aria-label={removeLabel ?? t.ui.listToolbar.removeFilter(label)}
        onClick={onRemove}
        className="rounded-full text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-primary focus-visible:outline-none"
      >
        <X size={13} />
      </button>
    </span>
  );
}

/** Clears every filter. Render it only while a filter is set. */
export function ResetFilters({ onReset }: { onReset: () => void }) {
  const t = useT();
  return (
    <button
      type="button"
      aria-label={t.ui.listToolbar.resetLabel}
      onClick={onReset}
      className="rounded px-1 text-xs text-brand-dim hover:text-brand focus-visible:ring-2 focus-visible:ring-primary focus-visible:outline-none"
    >
      {t.ui.listToolbar.reset}
    </button>
  );
}

/**
 * How many rows the filters leave: "3 of 6 agents", or "6 agents" when
 * nothing is filtered out or the unfiltered total is not known. `noun` is
 * the list's own count label (`n => '6 agents'`). The live region is always
 * rendered and only its text varies (empty while no filter is `active`), so
 * a screen reader announces the first filter too, not just the later ones.
 */
export function ResultCount({ active, shown, total, noun }: { active: boolean; shown: number; total?: number; noun: (n: number) => string }) {
  const t = useT();
  const text = !active ? '' : total === undefined || total === shown ? noun(shown) : t.ui.listToolbar.countOf(shown, noun(total));
  return (
    <span aria-live="polite" className="text-xs whitespace-nowrap text-muted-foreground">
      {text}
    </span>
  );
}
