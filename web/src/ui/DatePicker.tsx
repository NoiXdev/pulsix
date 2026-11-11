import { useCallback, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { DayPicker, type ClassNames, type Labels, type Matcher } from 'react-day-picker';
import { de } from 'react-day-picker/locale/de';
import { enUS } from 'react-day-picker/locale/en-US';
import { CalendarDays } from 'lucide-react';
import { cn } from '../lib/utils.ts';
import { useLocale, useT } from '../i18n/useT.ts';
import { useFormat } from '../i18n/format.ts';
import type { Locale } from '../i18n/types.ts';
import { Button } from './button.tsx';
import { Popover, type PopoverCloseReason } from './Popover.tsx';
import { parseIsoDate, toIsoDate } from './pickerValue.ts';

export interface DatePickerProps {
  /** 'YYYY-MM-DD', local, or '' for no date. */
  value: string;
  onChange: (value: string) => void;
  /** Lands on the trigger button, so a `<Label htmlFor>` names it. */
  id?: string;
  'aria-label'?: string;
  /** Takes part in native form validation: an empty value blocks submit. */
  required?: boolean;
  disabled?: boolean;
  /** Earliest and latest pickable day, 'YYYY-MM-DD'. */
  min?: string;
  max?: string;
  placeholder?: string;
  /** Marks the field invalid with this message — for a parent that knows
   *  something the field cannot (DateTimePicker: a time with no date).
   *  Blocks a surrounding form's submit while set. */
  invalidMessage?: string;
}

/** The calendar language and first weekday for each GUI language:
 *  Monday in German, Sunday in (US) English. */
const CALENDAR: Record<Locale, { locale: typeof de; weekStartsOn: 0 | 1 }> = {
  de: { locale: de, weekStartsOn: 1 },
  en: { locale: enUS, weekStartsOn: 0 },
};

const NAV_BUTTON =
  'inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground aria-disabled:pointer-events-none aria-disabled:opacity-30';

/** The calendar drawn in the app's own tokens; react-day-picker's stylesheet
 *  is not loaded. Modifier keys (selected, today, …) land on the day's cell,
 *  so they style the button inside it. */
const CLASS_NAMES: Partial<ClassNames> = {
  root: 'text-xs',
  months: 'relative',
  month: 'space-y-1',
  month_caption: 'flex h-7 items-center justify-center',
  caption_label: 'text-xs font-semibold',
  nav: 'absolute inset-x-0 top-0 flex h-7 items-center justify-between',
  button_previous: NAV_BUTTON,
  button_next: NAV_BUTTON,
  chevron: 'size-3.5 fill-current',
  month_grid: 'border-collapse',
  weekday: 'size-8 text-[11px] font-medium text-muted-foreground',
  day: 'p-0 text-center',
  day_button:
    'inline-flex size-8 items-center justify-center rounded-md hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:pointer-events-none',
  selected: '[&>button]:bg-primary [&>button]:font-semibold [&>button]:text-primary-foreground [&>button]:hover:bg-primary',
  today: '[&>button]:ring-1 [&>button]:ring-primary',
  disabled: 'opacity-30',
  outside: 'opacity-50',
  hidden: 'invisible',
};

const FIELD_DATE: Intl.DateTimeFormatOptions = { day: '2-digit', month: '2-digit', year: 'numeric' };
const DAY_NAME: Intl.DateTimeFormatOptions = { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' };
const MONTH_NAME: Intl.DateTimeFormatOptions = { month: 'long', year: 'numeric' };

/** A control that takes part in constraint validation. */
type Validated = Element & { willValidate: boolean; validity: ValidityState };

const isValidated = (el: Element): el is Validated => 'validity' in el && 'willValidate' in el;

/** True when `control` is the first invalid control of its form — the one
 * a browser would focus after a blocked submit. */
function isFirstInvalid(control: HTMLInputElement): boolean {
  const form = control.form;
  if (!form) return true;
  const first = Array.from(form.elements).find((el) => isValidated(el) && el.willValidate && !el.validity.valid);
  return first === undefined || first === control;
}

/**
 * DatePicker replaces the native date input: a field-shaped button that
 * shows the date the way the GUI language writes it and opens a calendar.
 *
 * The value is never re-written behind anyone's back: it changes only when
 * a day, Today or Clear is chosen, so opening and closing the calendar
 * leaves an existing value byte for byte (Review Focus 5). A value that is
 * not a real 'YYYY-MM-DD' day is shown as it is rather than dropped.
 *
 * Keyboard: Enter or Space on the field opens the calendar with the chosen
 * day (or today) focused; the arrows, PageUp/PageDown and Home/End move;
 * Enter picks; Escape closes it and returns to the field.
 *
 * A submit it blocks (required and empty, or `invalidMessage`) focuses the
 * field, marks it aria-invalid and shows why under it, linked through
 * aria-describedby, until the field is fine again. A required field says so
 * in its description too, as a button cannot carry aria-required.
 */
export function DatePicker({
  value, onChange, id, required, disabled, min, max, placeholder, invalidMessage,
  'aria-label': ariaLabel,
}: DatePickerProps) {
  const t = useT();
  const { locale } = useLocale();
  const fmt = useFormat();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const validationRef = useRef<HTMLInputElement>(null);
  const popoverId = useId();
  const textId = useId();
  const messageId = useId();
  const requiredId = useId();

  const selected = parseIsoDate(value);
  const minDate = parseIsoDate(min);
  const maxDate = parseIsoDate(max);
  const text = selected ? fmt.date(selected, FIELD_DATE) : value;

  const outOfRange: Matcher[] = [];
  if (minDate) outOfRange.push({ before: minDate });
  if (maxDate) outOfRange.push({ after: maxDate });

  const today = new Date();
  const todayIso = toIsoDate(today);
  // 'YYYY-MM-DD' strings order the same way as the days they name.
  const todayAllowed = (!minDate || todayIso >= toIsoDate(minDate)) && (!maxDate || todayIso <= toIsoDate(maxDate));
  const firstMonth = selected ?? (maxDate && today > maxDate ? maxDate : minDate && today < minDate ? minDate : today);

  // Every close but a press elsewhere hands focus back to the field: Safari
  // does not focus a button it clicks, so closing by the trigger would
  // otherwise leave focus on <body>, the calendar's day having gone.
  const close = useCallback((reason: PopoverCloseReason | 'picked' | 'trigger') => {
    setOpen(false);
    if (reason !== 'outside') triggerRef.current?.focus();
  }, []);

  // The calendar's names come from the catalogue, not the library's
  // English: the day as the GUI language writes it in full, with our own
  // "Today" and "selected" around it.
  const labels = useMemo<Partial<Labels>>(() => ({
    labelNav: () => t.ui.picker.monthNav,
    labelPrevious: () => t.ui.picker.previousMonth,
    labelNext: () => t.ui.picker.nextMonth,
    labelGrid: (month) => fmt.date(month, MONTH_NAME),
    labelWeekday: (day) => fmt.date(day, { weekday: 'long' }),
    labelDayButton: (day, modifiers) => {
      let name = fmt.date(day, DAY_NAME);
      if (modifiers.today) name = t.ui.picker.dayToday(name);
      if (modifiers.selected) name = t.ui.picker.daySelected(name);
      return name;
    },
  }), [t, fmt]);

  const invalid = invalidMessage !== undefined;
  // What the validation proxy below refuses, worked out the way it does:
  // its only constraints are `required` and the custom validity.
  const missing = !!required && value === '';
  const refused = !disabled && (invalid || missing);
  const refusal = invalidMessage ?? (missing ? t.ui.picker.required : undefined);
  // A blocked submit reports the refusal next to the field, until the
  // field is fine again; a later refusal waits for the next submit.
  const [reported, setReported] = useState(false);
  if (reported && !refused) setReported(false);
  const message = reported ? refusal : undefined;
  useLayoutEffect(() => {
    validationRef.current?.setCustomValidity(invalidMessage ?? '');
  }, [invalidMessage]);

  const pick = (next: string) => {
    if (next !== value) onChange(next);
    close('picked');
  };

  return (
    <div>
      <div className="relative">
        {/* Native validation: `required`, and `invalidMessage` as a custom
            validity. Not hidden (type=hidden) nor readonly: both are barred
            from constraint validation. Sits under the trigger, out of the
            tab order; a blocked submit is reported on the trigger instead
            (onInvalid below). */}
        <input
          ref={validationRef}
          tabIndex={-1}
          aria-hidden
          required={required}
          // Disabled like the field it stands for: a disabled control is
          // barred from constraint validation, so a greyed-out field never
          // blocks the surrounding form's submit.
          disabled={disabled}
          value={value}
          onChange={() => {}}
          // The browser would focus this invisible input and point its bubble
          // at it, where a keyboard user sees no focus ring and a screen
          // reader hears nothing. Cancelled, it does neither and the submit
          // stays blocked; the field says why itself, on its trigger.
          onInvalid={(e) => {
            e.preventDefault();
            setReported(true);
            const proxy = e.currentTarget;
            const trigger = triggerRef.current;
            if (!trigger || !isFirstInvalid(proxy)) return;
            trigger.focus();
            // A browser still focuses the first invalid control nobody
            // cancelled, even one after this (a required DateTimePicker's
            // empty time box): take the focus back once it has.
            window.setTimeout(() => {
              const active = document.activeElement;
              if (active && active !== trigger && proxy.form?.contains(active) && isValidated(active) && !active.validity.valid) {
                trigger.focus();
              }
            });
          }}
          className="pointer-events-none absolute inset-x-0 bottom-0 h-px opacity-0"
        />
        <button
          ref={triggerRef}
          type="button"
          id={id}
          aria-label={ariaLabel}
          // aria-required is not allowed on a button, so "Required" is
          // part of the description instead; the name stays the label.
          aria-describedby={[text && textId, required && requiredId, message && messageId].filter(Boolean).join(' ') || undefined}
          aria-invalid={invalid || message !== undefined || undefined}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-controls={open ? popoverId : undefined}
          disabled={disabled}
          onClick={() => (open ? close('trigger') : setOpen(true))}
          onKeyDown={(e: KeyboardEvent) => {
            if (e.key === 'Escape' && open) {
              e.preventDefault();
              setOpen(false);
            }
          }}
          className={cn(
            'flex h-8 w-full items-center justify-between gap-2 rounded-md border border-border bg-background px-2.5 text-left text-xs text-foreground focus-visible:border-primary focus-visible:outline-none disabled:opacity-50',
            (invalid || message !== undefined) && 'border-destructive',
          )}
        >
          <span id={textId} className={cn('truncate', !text && 'text-muted-foreground')}>
            {text || placeholder}
          </span>
          <CalendarDays size={14} className="shrink-0 text-muted-foreground" aria-hidden />
        </button>
        {required && (
          <span id={requiredId} className="sr-only">
            {t.ui.picker.requiredHint}
          </span>
        )}
        {open && !disabled && (
          <Popover anchor={triggerRef} onClose={close} id={popoverId} label={t.ui.picker.calendar}>
            <DayPicker
              mode="single"
              required
              selected={selected}
              onSelect={(d) => pick(toIsoDate(d))}
              defaultMonth={firstMonth}
              startMonth={minDate}
              endMonth={maxDate}
              disabled={outOfRange}
              locale={CALENDAR[locale].locale}
              weekStartsOn={CALENDAR[locale].weekStartsOn}
              classNames={CLASS_NAMES}
              labels={labels}
              autoFocus
            />
            <div className="mt-1 flex items-center justify-between border-t border-border pt-2">
              <Button type="button" variant="ghost" disabled={!todayAllowed} onClick={() => pick(todayIso)}>
                {t.ui.picker.today}
              </Button>
              {!required && value !== '' && (
                <Button type="button" variant="ghost" onClick={() => pick('')}>
                  {t.ui.picker.clear}
                </Button>
              )}
            </div>
          </Popover>
        )}
      </div>
      {message && (
        <p id={messageId} className="mt-1 text-[11px] text-destructive">
          {message}
        </p>
      )}
    </div>
  );
}
