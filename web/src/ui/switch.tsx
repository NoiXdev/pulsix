import { cn } from '../lib/utils.ts';

export interface SwitchProps {
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
  id?: string;
  'aria-label'?: string;
  'aria-describedby'?: string;
  name?: string;
}

/** Switch — the control for a boolean setting (use a checkbox only to pick
 * members of a list). A native button, so Space and Enter toggle it; the
 * mockup size is 34x19 px with a 15 px knob. */
export function Switch({ checked, onCheckedChange, disabled, ...rest }: SwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onCheckedChange(!checked)}
      className={cn(
        'inline-flex h-[19px] w-[34px] shrink-0 items-center rounded-full px-[2px] transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        'disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'bg-primary' : 'bg-border',
      )}
      {...rest}
    >
      <span
        aria-hidden="true"
        className={cn('h-[15px] w-[15px] rounded-full bg-background transition-transform', checked ? 'translate-x-[15px]' : 'translate-x-0')}
      />
    </button>
  );
}
