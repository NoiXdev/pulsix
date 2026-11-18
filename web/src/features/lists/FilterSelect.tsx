import { useId } from 'react';
import { Select, type SelectOption } from '../../ui/Select.tsx';

export interface FilterSelectProps {
  label: string;
  options: SelectOption[];
  /** The chosen value; '' or undefined shows `allLabel`. */
  value: string | undefined;
  /** Called with the new value, or undefined for "all". */
  onChange: (value: string | undefined) => void;
  /** The empty choice's text ("All zones"). */
  allLabel: string;
}

/**
 * A labelled picker for a list filter ("Zone: All zones"). The label is a
 * sibling of the Select with htmlFor carrying the association: an option
 * row inside Select's popover must not sit inside a label (a click would
 * reopen the list it just closed).
 */
export function FilterSelect({ label, options, value, onChange, allLabel }: FilterSelectProps) {
  const id = useId();
  return (
    <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <label htmlFor={id}>{label}</label>
      <Select id={id} options={options} value={value ?? ''} onChange={(v) => onChange(v || undefined)} placeholder={allLabel} />
    </div>
  );
}
