import { useCallback, useId, useRef, useState, type KeyboardEvent } from 'react';
import { DayPicker, type ClassNames, type Matcher } from 'react-day-picker';
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
 */
export function DatePicker({
  value, onChange, id, required, disabled, min, max, placeholder,
  'aria-label': ariaLabel,
}: DatePickerProps) {
  const t = useT();
  const { locale } = useLocale();
  const fmt = useFormat();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popoverId = useId();
  const textId = useId();

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

  const close = useCallback((reason: PopoverCloseReason | 'picked') => {
    setOpen(false);
    if (reason !== 'outside') triggerRef.current?.focus();
  }, []);

  const pick = (next: string) => {
    if (next !== value) onChange(next);
    close('picked');
  };

  return (
    <div className="relative">
      {/* Native validation for `required`. Not hidden (type=hidden) nor
          readonly: both are barred from constraint validation. Sits under
          the trigger, first in the tab order around it, so the browser's
          "fill in this field" bubble points at the field and Tab from it
          lands on the trigger. */}
      {required && (
        <input
          tabIndex={-1}
          aria-hidden
          required
          value={value}
          onChange={() => {}}
          className="pointer-events-none absolute inset-x-0 bottom-0 h-px opacity-0"
        />
      )}
      <button
        ref={triggerRef}
        type="button"
        id={id}
        aria-label={ariaLabel}
        aria-describedby={text ? textId : undefined}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? popoverId : undefined}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e: KeyboardEvent) => {
          if (e.key === 'Escape' && open) {
            e.preventDefault();
            setOpen(false);
          }
        }}
        className="flex h-8 w-full items-center justify-between gap-2 rounded-md border border-border bg-background px-2.5 text-left text-xs text-foreground focus-visible:border-primary focus-visible:outline-none disabled:opacity-50"
      >
        <span id={textId} className={cn('truncate', !text && 'text-muted-foreground')}>
          {text || placeholder}
        </span>
        <CalendarDays size={14} className="shrink-0 text-muted-foreground" aria-hidden />
      </button>
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
  );
}
