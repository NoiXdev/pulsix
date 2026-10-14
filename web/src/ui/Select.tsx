import { useCallback, useMemo, useState } from 'react';
import { useCombobox } from 'downshift';
import { ChevronDown } from 'lucide-react';
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

/** Stands in for "no value" when a placeholder is given: downshift wants
 *  an item, and null already means "nothing is selected". */
const NONE = ' none';

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
  // `create` is unread until Task 2 adds the "+ Create…" row. Declaring it
  // in the props now (per the Task 1 interface) without touching it here
  // trips noUnusedParameters; `void` is this codebase's existing way to
  // park a deliberately-unused binding (see RunDetailPage.test.tsx).
  void create;

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

  const rows = useMemo<SelectOption[]>(() => toRows(filter), [toRows, filter]);

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
        {open && rows.length === 0 && (
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
              )}
            >
              <span className="flex-1 truncate">{o.label}</span>
              {o.hint && <span className="shrink-0 text-[11px] text-muted-foreground">{o.hint}</span>}
            </li>
          ))}
      </ul>
    </div>
  );
}
