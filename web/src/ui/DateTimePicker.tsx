import { useId, useState } from 'react';
import { useT } from '../i18n/useT.ts';
import { DatePicker } from './DatePicker.tsx';
import { TimeField } from './TimeField.tsx';
import { joinDateTime, splitDateTime, type DateTimeParts } from './pickerValue.ts';

export interface DateTimePickerProps {
  /** 'YYYY-MM-DDTHH:mm', local, or '' for nothing chosen. */
  value: string;
  onChange: (value: string) => void;
  /** Lands on the date's trigger button, so a `<Label htmlFor>` names it.
   *  The time box is then named after that label too ("From Time"). */
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
 * marked invalid and the form cannot be submitted, as with the native input
 * — except while disabled: a greyed-out field never blocks a submit.
 * Clearing the day clears the time with it, so an optional field is left
 * empty rather than half-filled, and so does disabling the field.
 *
 * The time box carries the field's name as well: from `aria-label`
 * ("Starts, time"), or, with an `id` and no `aria-label`, from the
 * `<Label htmlFor={id}>` naming the day plus a hidden "Time" ("From Time").
 */
export function DateTimePicker({
  value, onChange, id, required, disabled, min, max, placeholder,
  'aria-label': ariaLabel,
}: DateTimePickerProps) {
  const t = useT();
  const timeLabelId = useId();
  const [parts, setParts] = useState(() => splitDateTime(value));

  // A value changed from outside replaces both halves — unless it is the
  // one these halves already make (including the '' a half-filled pair
  // emits), so a half typed in is not thrown away by its own echo.
  const [seen, setSeen] = useState(value);
  if (value !== seen) {
    setSeen(value);
    if (value !== joinDateTime(parts)) setParts(splitDateTime(value));
  }

  // Disabling the field while it shows only half a value drops that half:
  // greyed out, it has no say in the form (see `incomplete` below), and a
  // time left in the box would come back with the field (SnoozeDialog's
  // "until the severity changes"). The caller already reads '', so this is
  // no change of value and nothing is emitted.
  const [wasDisabled, setWasDisabled] = useState(!!disabled);
  if (!!disabled !== wasDisabled) {
    setWasDisabled(!!disabled);
    if (disabled && value === '' && (parts.date || parts.time)) setParts({ date: '', time: '' });
  }

  // Half a date-time reads '' to the caller, but it is not an empty field:
  // native datetime-local refuses it as badInput even when optional. Mark
  // the missing half, which also blocks a surrounding form's submit —
  // unless the field is disabled, which takes it out of the form's say.
  const incomplete = disabled ? undefined : t.ui.picker.incomplete;
  const dateMissing = !parts.date && parts.time ? incomplete : undefined;
  const timeMissing = parts.date && !parts.time ? incomplete : undefined;

  const timeName = ariaLabel
    ? { 'aria-label': t.ui.picker.timeOf(ariaLabel) }
    : id
      ? { 'aria-labelledby': `${id} ${timeLabelId}` }
      : { 'aria-label': t.ui.picker.time };

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
          onChange={(date) => update(date ? { date, time: parts.time || '00:00' } : { date: '', time: '' })}
          required={required}
          disabled={disabled}
          min={min ? splitDateTime(min).date : undefined}
          max={max ? splitDateTime(max).date : undefined}
          placeholder={placeholder}
          invalidMessage={dateMissing}
        />
      </div>
      <div className="w-20 shrink-0">
        {!ariaLabel && id && (
          <span id={timeLabelId} className="sr-only">
            {t.ui.picker.time}
          </span>
        )}
        <TimeField
          {...timeName}
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
