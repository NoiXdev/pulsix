import { useMemo } from 'react';
import { useLocale } from './useT.ts';
import type { Locale } from './types.ts';

type DateInput = Date | string | number;
type DateOptions = Intl.DateTimeFormatOptions & { timeZone?: string };

/**
 * Format is how the GUI writes dates, times and numbers in the active
 * language. Each date helper is the matching `toLocale*String` with the
 * locale filled in, so a call site that passed options before — a
 * `timeZone` above all (schedules, the calendar, digests) — passes the
 * same options here and keeps its zone in either language.
 */
export interface Format {
  /** `toLocaleDateString` — "11/2/2026" / "2.11.2026" with no options. */
  date(d: DateInput, opts?: DateOptions): string;
  /** `toLocaleString` — date and time. */
  dateTime(d: DateInput, opts?: DateOptions): string;
  /** `toLocaleTimeString`. */
  time(d: DateInput, opts?: DateOptions): string;
  /** `toLocaleString` for a number — "1,234.5" / "1.234,5". */
  number(n: number, opts?: Intl.NumberFormatOptions): string;
  /** How long ago (or from now) `d` is, in the largest whole unit:
   * "5 minutes ago" / "vor 5 Minuten", "yesterday" / "gestern". */
  relative(d: DateInput, now?: Date): string;
}

const toDate = (d: DateInput) => (d instanceof Date ? d : new Date(d));

const UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
  ['second', 1],
];

/**
 * formatter binds Format to one locale. Components use useFormat();
 * plain helper modules (a `.ts` that builds a label outside React) take a
 * Format as a parameter and let their caller pass useFormat()'s result.
 */
export function formatter(locale: Locale): Format {
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
  return {
    date: (d, opts) => toDate(d).toLocaleDateString(locale, opts),
    dateTime: (d, opts) => toDate(d).toLocaleString(locale, opts),
    time: (d, opts) => toDate(d).toLocaleTimeString(locale, opts),
    number: (n, opts) => n.toLocaleString(locale, opts),
    relative: (d, now = new Date()) => {
      const seconds = (toDate(d).getTime() - now.getTime()) / 1000;
      const [unit, size] = UNITS.find(([, s]) => Math.abs(seconds) >= s) ?? ['second', 1];
      return rtf.format(Math.round(seconds / size), unit);
    },
  };
}

/** useFormat is formatter() for the active language. */
export function useFormat(): Format {
  const { locale } = useLocale();
  return useMemo(() => formatter(locale), [locale]);
}
