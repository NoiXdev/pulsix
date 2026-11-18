import {
  useState,
  type ChangeEvent,
  type ClipboardEvent,
  type Ref,
  type SyntheticEvent,
} from 'react';
import { cn } from '../lib/utils.ts';

export interface PinInputProps {
  id: string;
  value: string;
  onChange: (digits: string) => void;
  /** Fires once each time the value becomes complete. */
  onComplete?: (digits: string) => void;
  length?: number;
  autoFocus?: boolean;
  disabled?: boolean;
  invalid?: boolean;
  /** Plays a short horizontal shake (motion-safe only). */
  shake?: boolean;
  autoComplete?: string;
  /** Opts the field out of password managers (1Password, LastPass, Bitwarden). */
  noManagers?: boolean;
  'aria-describedby'?: string;
  inputRef?: Ref<HTMLInputElement>;
}

/** Four-box PIN input. One real password input lies transparent over the
 * boxes, so paste, password managers, the label and screen readers treat it
 * as a single field; the aria-hidden boxes are drawn from `value`. */
export function PinInput({
  id,
  value,
  onChange,
  onComplete,
  length = 4,
  autoFocus,
  disabled,
  invalid,
  shake,
  autoComplete = 'off',
  noManagers,
  'aria-describedby': describedBy,
  inputRef,
}: PinInputProps) {
  const [focused, setFocused] = useState(false);
  // The box the next digit goes into; a full PIN keeps the last one marked,
  // so a focused field always shows where the focus is.
  const active = focused && !disabled ? Math.min(value.length, length - 1) : -1;

  function commit(raw: string) {
    const digits = raw.replace(/\D/g, '').slice(0, length);
    onChange(digits);
    if (digits.length === length && value.length !== length)
      onComplete?.(digits);
  }

  function handleChange(e: ChangeEvent<HTMLInputElement>) {
    commit(e.target.value);
  }

  // maxLength would cut a paste before the non-digits are dropped
  // ('12a45' -> '12a4' -> '124'), so filter the clipboard text ourselves.
  function handlePaste(e: ClipboardEvent<HTMLInputElement>) {
    e.preventDefault();
    const pasted = e.clipboardData.getData('text').replace(/\D/g, '');
    // A whole PIN replaces what is typed; a fragment continues it.
    commit(pasted.length >= length ? pasted : value + pasted);
  }

  // Keep the caret at the end: no insertion in the middle of the PIN.
  function pinCaret(e: SyntheticEvent<HTMLInputElement>) {
    const el = e.currentTarget;
    const end = el.value.length;
    if (el.selectionStart !== end || el.selectionEnd !== end)
      el.setSelectionRange(end, end);
  }

  return (
    <div
      className={cn(
        'relative inline-flex gap-3',
        shake && 'motion-safe:animate-pin-shake',
      )}
    >
      {Array.from({ length }, (_, i) => {
        const isFilled = i < value.length;
        return (
          <div
            key={i}
            aria-hidden="true"
            data-pin-box
            data-filled={isFilled}
            data-active={i === active}
            className={cn(
              'relative flex h-16 w-14 items-center justify-center rounded-xl border bg-background',
              invalid ? 'border-destructive' : 'border-border',
              i === active && 'border-primary ring-2 ring-primary',
              disabled && 'opacity-50',
            )}
          >
            {isFilled && (
              <span className="h-3.5 w-3.5 rounded-full bg-foreground" />
            )}
            {i === active && !isFilled && (
              <span className="absolute h-6 w-0.5 bg-primary motion-safe:animate-pin-caret" />
            )}
          </div>
        );
      })}
      <input
        id={id}
        ref={inputRef}
        type="password"
        inputMode="numeric"
        maxLength={length}
        value={value}
        autoFocus={autoFocus}
        disabled={disabled}
        autoComplete={autoComplete}
        {...(noManagers ? { 'data-1p-ignore': true, 'data-lpignore': 'true', 'data-bwignore': true } : {})}
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
        onChange={handleChange}
        onPaste={handlePaste}
        onSelect={pinCaret}
        onFocus={(e) => {
          setFocused(true);
          pinCaret(e);
        }}
        onBlur={() => setFocused(false)}
        className="absolute inset-0 h-full w-full cursor-text bg-transparent text-[16px] text-transparent caret-transparent opacity-0 focus:outline-none disabled:cursor-not-allowed"
      />
    </div>
  );
}
