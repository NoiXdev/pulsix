import { Select, type SelectOption } from '../../ui/Select.tsx';

export interface FilterSelectProps {
  /** The picker's accessible name ("Filter by plugin"). */
  label: string;
  options: SelectOption[];
  /** The chosen value; '' or undefined shows `allLabel`. */
  value: string | undefined;
  /** Called with the new value, or undefined for "all". */
  onChange: (value: string | undefined) => void;
  /** The empty choice's text ("All plugins"), shown when nothing is chosen. */
  allLabel: string;
}

/**
 * The one picker every list's filter bar uses for a filter with a fixed set
 * of values: named for assistive technology and showing its empty choice
 * ("All zones", "Any status") as the placeholder. The name should say
 * "Filter by …" so it never clashes with a form field of the same name in a
 * dialog above the list.
 */
export function FilterSelect({ label, options, value, onChange, allLabel }: FilterSelectProps) {
  return <Select aria-label={label} options={options} value={value ?? ''} onChange={(v) => onChange(v || undefined)} placeholder={allLabel} />;
}
