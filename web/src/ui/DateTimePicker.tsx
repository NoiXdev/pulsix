import { useState } from 'react';
import { useT } from '../i18n/useT.ts';
import { DatePicker } from './DatePicker.tsx';
import { TimeField } from './TimeField.tsx';
import { joinDateTime, splitDateTime, type DateTimeParts } from './pickerValue.ts';

export interface DateTimePickerProps {
  /** 'YYYY-MM-DDTHH:mm', local, or '' for nothing chosen. */
  value: string;
  onChange: (value: string) => void;
  /** Lands on the date's trigger button, so a `<Label htmlFor>` names it. */
  id?: string;
  'aria-label'?: string;
  required?: boolean;
  disabled?: boolean;
  /** Earliest and latest moment, 'YYYY-MM-DDTHH:mm'. Only their days limit
   *  the calendar; the time of day is not constrained. */
  min?: string;
  max?: string;
  placeholder?: string;
}

/**
 * DateTimePicker replaces the native datetime-local input: a DatePicker and
 * a TimeField side by side, joined into one 'YYYY-MM-DDTHH:mm' value.
 *
 * It holds the two halves itself, because the value contract has no
 * half-filled state: a time typed before any day is picked, or a day kept
 * while its time is cleared and retyped, lives here until both halves are
 * there again, while the value meanwhile reads ''. A day picked with no time
 * yet starts at 00:00. While exactly one half is filled, the other is
 * marked invalid and the form cannot be submitted, as with the native input.
 */
export function DateTimePicker({
  value, onChange, id, required, disabled, min, max, placeholder,
  'aria-label': ariaLabel,
}: DateTimePickerProps) {
  const t = useT();
  const [parts, setParts] = useState(() => splitDateTime(value));

  // A value changed from outside replaces both halves — unless it is the
  // one these halves already make (including the '' a half-filled pair
  // emits), so a half typed in is not thrown away by its own echo.
  const [seen, setSeen] = useState(value);
  if (value !== seen) {
    setSeen(value);
    if (value !== joinDateTime(parts)) setParts(splitDateTime(value));
  }

  // Half a date-time reads '' to the caller, but it is not an empty field:
  // native datetime-local refuses it as badInput even when optional. Mark
  // the missing half, which also blocks a surrounding form's submit.
  const incomplete = t.ui.picker.incomplete;
  const dateMissing = !parts.date && parts.time ? incomplete : undefined;
  const timeMissing = parts.date && !parts.time ? incomplete : undefined;

  const update = (next: DateTimeParts) => {
    setParts(next);
    const joined = joinDateTime(next);
    if (joined !== value) onChange(joined);
  };

  return (
    <div className="flex gap-2">
      <div className="min-w-0 flex-1">
        <DatePicker
          id={id}
          aria-label={ariaLabel}
          value={parts.date}
          onChange={(date) => update({ date, time: date && !parts.time ? '00:00' : parts.time })}
          required={required}
          disabled={disabled}
          min={min ? splitDateTime(min).date : undefined}
          max={max ? splitDateTime(max).date : undefined}
          placeholder={placeholder}
          invalidMessage={dateMissing}
        />
      </div>
      <div className="w-20 shrink-0">
        <TimeField
          aria-label={ariaLabel ? t.ui.picker.timeOf(ariaLabel) : t.ui.picker.time}
          value={parts.time}
          onChange={(time) => update({ ...parts, time })}
          required={required}
          disabled={disabled}
          invalidMessage={timeMissing}
        />
      </div>
    </div>
  );
}
