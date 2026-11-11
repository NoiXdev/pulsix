import { useLayoutEffect, useRef, useState } from 'react';
import { cn } from '../lib/utils.ts';
import { useT } from '../i18n/useT.ts';
import { Input } from './input.tsx';
import { isTimeShape, parseTime } from './pickerValue.ts';

export interface TimeFieldProps {
  /** 'HH:mm' on the 24-hour clock, or '' for no time. */
  value: string;
  onChange: (value: string) => void;
  /** Lands on the input, so a `<Label htmlFor>` names it. */
  id?: string;
  'aria-label'?: string;
  'aria-labelledby'?: string;
  required?: boolean;
  disabled?: boolean;
  /** Marks the field invalid with this message, whatever is in it — for a
   *  parent that knows something the field cannot (DateTimePicker: a date
   *  with no time). Blocks a surrounding form's submit while set. */
  invalidMessage?: string;
}

/**
 * TimeField replaces the native time input with a text field that reads a
 * time the way people type one ('930', '9:30', '9.30', '0930') and writes
 * it back as 'HH:mm', on the 24-hour clock in every language — the browser's
 * own time input followed the OS's 12/24-hour setting instead of the GUI's.
 *
 * It never emits a malformed value (Review Focus 3). While typing it emits
 * only '' (cleared) or a complete, valid 'HH:mm'; anything shorter waits for
 * leaving the field or Enter, which normalises it. An entry that is not a
 * time keeps the last valid value, is marked aria-invalid, and carries a
 * custom validity message, so a surrounding form cannot submit it.
 */
export function TimeField({
  value, onChange, id, required, disabled, invalidMessage,
  'aria-label': ariaLabel, 'aria-labelledby': ariaLabelledBy,
}: TimeFieldProps) {
  const t = useT();
  const ref = useRef<HTMLInputElement>(null);
  const [draft, setDraft] = useState(value);
  const [invalid, setInvalid] = useState(false);

  // A value changed from outside replaces what is in the field — unless it
  // is the value this field itself just emitted for the draft still in it
  // ('0930' stays as typed until the field is left). React's pattern for
  // state that follows a prop: adjust it during render.
  const [seen, setSeen] = useState(value);
  if (value !== seen) {
    setSeen(value);
    const current = draft.trim() === '' ? '' : parseTime(draft);
    if (value !== current) {
      setDraft(value);
      setInvalid(false);
    }
  }

  const unreadable = draft.trim() !== '' && parseTime(draft) === null;
  const message = t.ui.picker.invalidTime;
  // An unreadable entry outranks the parent's message: it is the more
  // specific thing to fix.
  const validity = unreadable ? message : (invalidMessage ?? '');
  useLayoutEffect(() => {
    ref.current?.setCustomValidity(validity);
  }, [validity]);

  const emit = (next: string) => {
    if (next !== value) onChange(next);
  };

  const commit = () => {
    if (draft.trim() === '') return;
    const parsed = parseTime(draft);
    if (parsed === null) {
      setInvalid(true);
      return;
    }
    setDraft(parsed);
    setInvalid(false);
    emit(parsed);
  };

  return (
    <Input
      ref={ref}
      type="text"
      // Decimal, not numeric: iOS's numeric pad has no separator key, and
      // '9.30' is a time this field reads.
      inputMode="decimal"
      autoComplete="off"
      maxLength={5}
      id={id}
      aria-label={ariaLabel}
      aria-labelledby={ariaLabelledBy}
      aria-invalid={invalid || invalidMessage !== undefined || undefined}
      required={required}
      disabled={disabled}
      placeholder={t.ui.picker.timePlaceholder}
      value={draft}
      onChange={(e) => {
        const text = e.target.value;
        setDraft(text);
        if (text.trim() === '') {
          setInvalid(false);
          emit('');
          return;
        }
        if (!isTimeShape(text)) {
          setInvalid(false);
          return;
        }
        const parsed = parseTime(text);
        setInvalid(parsed === null);
        if (parsed !== null) emit(parsed);
      }}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') commit();
      }}
      className={cn('tabular-nums', (invalid || invalidMessage !== undefined) && 'border-destructive')}
    />
  );
}
