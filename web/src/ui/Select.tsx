import { useCallback, useMemo, useState, type KeyboardEvent } from 'react';
import { useCombobox } from 'downshift';
import { ChevronDown, Plus } from 'lucide-react';
import { cn } from '../lib/utils.ts';

export interface SelectOption {
  value: string;
  label: string;
  /** Secondary text on the row — a credential's kind, a host's zone. */
  hint?: string;
  disabled?: boolean;
}

export interface SelectProps {
  options: SelectOption[];
  value: string;
  onChange: (value: string) => void;
  /** Shown as the first row and as the empty label; its presence is what
   *  lets the field go back to no value at all. */
  placeholder?: string;
  id?: string;
  'aria-label'?: string;
  disabled?: boolean;
  invalid?: boolean;
  create?: { label: string; onSelect: (filterText: string) => void };
}

/**
 * downshift's callAllEventHandlers() — used internally by getInputProps —
 * runs the onKeyDown handed to it before downshift's own, and skips its own
 * entirely when this flag turns up set on the event afterwards. That is
 * downshift's own documented escape hatch, not something private being
 * reached into; there is just no exported type for the flag, so this
 * declares only enough of the shape to set it without `any`.
 */
interface DownshiftEscapeHatchEvent extends KeyboardEvent<Element> {
  preventDownshiftDefault?: boolean;
}

/** Stands in for "no value" when a placeholder is given: downshift wants
 *  an item, and null already means "nothing is selected". */
const NONE = ' none';

/** Stands in for the create row, the same way NONE stands in for "no
 *  value": downshift needs a real item to select, and the row has no
 *  SelectOption of its own in `options`. */
const CREATE = ' create';

/**
 * Select is the one picker Pulsix is standardising on (v0.4g, backlog 1.43):
 * it will replace the SPA's 76 native select elements over the tasks that
 * follow this one, and Task 9 adds the ESLint rule that bans a new native
 * <select> once that migration lands.
 *
 * Always a combobox, never a plain listbox, and with no threshold that
 * switches between the two: options arrive from queries, so a threshold
 * would let the widget change its shape and its keyboard model while the
 * user is inside it.
 *
 * Presentational, like everything in src/ui: it is handed its options and
 * fetches nothing, and `create` is a callback into the feature that owns
 * the dialog (backlog 3.23, already enforced by the no-restricted-imports
 * rule on src/ui/**).
 */
export function Select({
  options, value, onChange, placeholder, id, disabled, invalid, create,
  'aria-label': ariaLabel,
}: SelectProps) {
  const [filter, setFilter] = useState('');

  // Shared by the rendered rows and by the stateReducer below, which needs
  // to re-filter from the keystroke's own value rather than from `filter`
  // React state: downshift dispatches InputChange against the props it
  // captured on the previous render, so reading the `rows` memo there would
  // be one keystroke behind.
  const toRows = useCallback(
    (q: string): SelectOption[] => {
      const base = placeholder ? [{ value: NONE, label: placeholder }, ...options] : options;
      const query = q.trim().toLowerCase();
      if (query === '') return base;
      return base.filter((o) => o.label.toLowerCase().includes(query) || (o.hint ?? '').toLowerCase().includes(query));
    },
    [options, placeholder],
  );

  // The create row is appended here, after toRows, rather than folded into
  // toRows itself: toRows is also what the stateReducer below recomputes
  // from a keystroke's own value to find the highlighted match, and the
  // create row must never be a candidate for that (spec decision 5) — kept
  // out of toRows, there is nothing to exclude there, see that case's
  // comment.
  const rows = useMemo<SelectOption[]>(() => {
    const matched = toRows(filter);
    return create ? [...matched, { value: CREATE, label: create.label }] : matched;
  }, [toRows, filter, create]);

  const selected = options.find((o) => o.value === value) ?? null;
  // Review Focus 2: a value whose option is gone (a deleted credential a
  // host still points at). Showing it is the honest answer; clearing it
  // would write a change nobody asked for on the next save.
  const selectedLabel = selected?.label ?? (value === '' ? (placeholder ?? '') : `${value} (unavailable)`);

  const cb = useCombobox<SelectOption>({
    items: rows,
    itemToString: (item) => item?.label ?? '',
    itemToKey: (item) => item?.value ?? null,
    selectedItem: selected,
    onSelectedItemChange: ({ selectedItem }) => {
      if (!selectedItem || selectedItem.disabled) return;
      if (selectedItem.value === CREATE) {
        create?.onSelect(filter.trim());
        setFilter('');
        return;
      }
      onChange(selectedItem.value === NONE ? '' : selectedItem.value);
      setFilter('');
    },
    isItemDisabled: (item) => item.disabled === true,
    stateReducer: (_state, { type, changes }) => {
      switch (type) {
        // downshift's own InputChange case always drops highlightedIndex to
        // -1 while the menu is open, so a query that narrows the list to one
        // row still leaves Enter with nothing to commit. Highlight the first
        // selectable match instead, computed from this keystroke's own value
        // rather than the `rows` memo: downshift dispatches against the
        // props it captured on the previous render, so `rows` would still be
        // the one from before this character was typed.
        //
        // This doubles as the create-row guard (spec decision 5): toRows
        // never includes the create row (see the `rows` memo above), so a
        // query that matches nothing yields an empty `fresh` here and
        // findIndex falls through to -1 — never the create row, which would
        // otherwise be the only item left for Enter to land on. Arrowing
        // onto it by hand still works, since that goes through downshift's
        // own ItemHighlighted handling, not this case.
        case useCombobox.stateChangeTypes.InputChange: {
          const fresh = toRows(changes.inputValue ?? '');
          return { ...changes, highlightedIndex: fresh.findIndex((o) => !o.disabled) };
        }
        // Review Focus 3: closing without a choice is not a choice. Drop
        // whatever was typed and let the selected label render again.
        case useCombobox.stateChangeTypes.InputBlur:
        case useCombobox.stateChangeTypes.InputKeyDownEscape:
          setFilter('');
          return changes;
        default:
          return changes;
      }
    },
  });

  const open = cb.isOpen && !disabled;

  return (
    <div className="relative">
      <input
        {...cb.getInputProps({
          id,
          'aria-label': ariaLabel,
          disabled,
          onChange: (e) => setFilter((e.target as HTMLInputElement).value),
          // A native <dialog>'s Escape-to-close is not a bubbling DOM
          // listener but a user-agent-level close request — stopPropagation
          // here would do nothing to it in a real browser. What the HTML
          // close-request steps actually honour is preventDefault() on the
          // keydown: cancelling it suppresses the dialog's close request
          // (every current browser implements this). So closing just this
          // list on Escape has to preventDefault, and only while the list is
          // open — once it's closed, Escape must reach the surrounding
          // dialog (ui/dialog.tsx has no Escape handling of its own) so a
          // second press can close that instead.
          onKeyDown: (e: KeyboardEvent<Element>) => {
            if (e.key !== 'Escape') return;
            if (cb.isOpen) {
              e.preventDefault();
              return;
            }
            // downshift's useCombobox prevents Escape's default whenever
            // *anything* is selected, open or not (its own Escape handler
            // ORs isOpen with selectedItem/inputValue/highlightedIndex) —
            // so a Select holding a value swallows every Escape keystroke
            // with the list already closed, including ones meant for a
            // surrounding Dialog. That is every edit dialog with a value
            // already chosen (RepoDialog, HostSshCard, WindowDialog): Escape
            // would do nothing, and nothing would explain why. Opt just
            // this closed-list case out of downshift's own handling so the
            // key reaches the dialog instead — a dialog that closes a touch
            // too eagerly is a smaller problem than one Escape cannot touch
            // at all.
            (e as DownshiftEscapeHatchEvent).preventDownshiftDefault = true;
          },
        })}
        value={open ? filter : selectedLabel}
        placeholder={placeholder}
        className={cn(
          'flex h-8 w-full rounded-md border border-border bg-background px-2.5 pr-7 text-xs text-foreground',
          'placeholder:text-muted-foreground focus-visible:border-primary focus-visible:outline-none disabled:opacity-50',
          invalid && 'border-destructive',
        )}
      />
      <button
        type="button"
        {...cb.getToggleButtonProps({ disabled, tabIndex: -1 })}
        aria-label={`Open ${ariaLabel ?? 'list'}`}
        className="absolute top-0 right-0 flex h-8 w-7 items-center justify-center text-muted-foreground"
      >
        <ChevronDown size={14} />
      </button>
      <ul
        {...cb.getMenuProps()}
        className={cn(
          'absolute z-50 mt-1 max-h-60 w-full overflow-y-auto rounded-md border border-border bg-card py-1 shadow-lg',
          !open && 'hidden',
        )}
      >
        {/* The create row is never filtered away (see the `rows` memo
            above), so a query that matches nothing still leaves it in
            `rows` — counting it out here keeps "No match" honest about the
            real options, not about the row that is always there. */}
        {open && rows.length === (create ? 1 : 0) && (
          <li className="px-2.5 py-1.5 text-xs text-muted-foreground">
            {options.length === 0 ? 'No options' : 'No match'}
          </li>
        )}
        {open &&
          rows.map((o, i) => (
            <li
              key={o.value}
              data-value={o.value === NONE ? '' : o.value}
              {...cb.getItemProps({ item: o, index: i, disabled: o.disabled })}
              className={cn(
                'flex cursor-pointer items-baseline gap-2 px-2.5 py-1.5 text-xs',
                cb.highlightedIndex === i && 'bg-background text-foreground',
                o.disabled && 'cursor-not-allowed opacity-50',
                o.value === CREATE && 'border-t border-border text-primary',
              )}
            >
              {o.value === CREATE && <Plus size={13} className="shrink-0" />}
              <span className="flex-1 truncate">{o.label}</span>
              {o.hint && <span className="shrink-0 text-[11px] text-muted-foreground">{o.hint}</span>}
            </li>
          ))}
      </ul>
    </div>
  );
}
