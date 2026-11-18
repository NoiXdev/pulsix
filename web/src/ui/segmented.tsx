import { useId } from 'react';
import { cn } from '../lib/utils.ts';

export interface SegmentedOption<T extends string> {
  value: T;
  label: string;
}

export interface SegmentedProps<T extends string> {
  /** The group's visible name (its legend). */
  label: string;
  options: readonly SegmentedOption<T>[];
  value: T;
  onChange: (value: T) => void;
  disabled?: boolean;
}

/**
 * Segmented is a choice between a few mutually exclusive values drawn as
 * one bar of buttons. Underneath it is a group of native radios, so the
 * arrow keys move the choice, a screen reader announces "radio, 1 of 3",
 * and a disabled ancestor <fieldset> disables it like any other input.
 */
export function Segmented<T extends string>({ label, options, value, onChange, disabled }: SegmentedProps<T>) {
  const name = useId();
  return (
    <fieldset disabled={disabled} className="flex min-w-0 flex-col gap-1">
      <legend className="mb-1 text-[11px] font-medium text-muted-foreground">{label}</legend>
      <div className="inline-flex self-start rounded-md border border-border bg-background p-0.5">
        {options.map((o) => {
          const checked = o.value === value;
          return (
            <label
              key={o.value}
              className={cn(
                'cursor-pointer rounded px-3 py-1 text-xs has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-primary has-[:disabled]:cursor-not-allowed',
                checked ? 'bg-primary/15 font-semibold text-foreground' : 'text-muted-foreground hover:text-foreground',
              )}
            >
              <input type="radio" className="sr-only" name={name} value={o.value} checked={checked} onChange={() => onChange(o.value)} />
              {o.label}
            </label>
          );
        })}
      </div>
    </fieldset>
  );
}
