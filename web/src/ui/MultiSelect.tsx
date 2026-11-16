import { X } from 'lucide-react';
import { Select, type SelectOption } from './Select.tsx';
import { useT } from '../i18n/useT.ts';

export interface MultiSelectProps {
  options: SelectOption[];
  /** The chosen values, in the order they were chosen. */
  value: string[];
  onChange: (value: string[]) => void;
  /** The picker's empty label: what choosing does ("Add a group"). */
  placeholder?: string;
  id?: string;
  'aria-label'?: string;
  'aria-describedby'?: string;
  /** Names the list of chosen entries; the field's own label otherwise. */
  listLabel?: string;
  disabled?: boolean;
  invalid?: boolean;
  /** No more than this many may be chosen; the picker is disabled at it. */
  max?: number;
}

/**
 * MultiSelect chooses several options: the chosen ones as a list of
 * removable entries, and a Select beneath that adds one more, offering
 * only the options not chosen yet. A chosen value the options do not hold
 * (a list that failed to load) stays listed under its own value, like
 * Select's unavailable value, so a save does not drop it silently.
 *
 * Presentational, like everything in src/ui: it is handed its options and
 * fetches nothing.
 */
export function MultiSelect({
  options, value, onChange, placeholder, id, disabled, invalid, max, listLabel,
  'aria-label': ariaLabel,
  'aria-describedby': ariaDescribedBy,
}: MultiSelectProps) {
  const t = useT();
  const labelOf = (v: string) => options.find((o) => o.value === v)?.label ?? t.ui.select.unavailable(v);
  const rest = options.filter((o) => !value.includes(o.value));
  const full = max !== undefined && value.length >= max;
  return (
    <div className="flex flex-col gap-1.5">
      {value.length > 0 && (
        <ul aria-label={listLabel ?? ariaLabel} className="flex flex-wrap gap-1.5">
          {value.map((v) => {
            const label = labelOf(v);
            return (
              <li key={v} className="inline-flex items-center gap-1 rounded-md border border-border bg-background py-0.5 pr-1 pl-2 text-xs">
                <span>{label}</span>
                <button
                  type="button"
                  className="rounded p-0.5 text-muted-foreground hover:text-foreground disabled:opacity-50"
                  aria-label={t.ui.multiSelect.remove(label)}
                  disabled={disabled}
                  onClick={() => onChange(value.filter((x) => x !== v))}
                >
                  <X className="h-3 w-3" aria-hidden />
                </button>
              </li>
            );
          })}
        </ul>
      )}
      <Select
        id={id}
        aria-label={ariaLabel}
        aria-describedby={ariaDescribedBy}
        placeholder={placeholder}
        options={rest}
        value=""
        disabled={disabled || full}
        invalid={invalid}
        onChange={(v) => {
          if (v !== '' && !value.includes(v)) onChange([...value, v]);
        }}
      />
    </div>
  );
}
