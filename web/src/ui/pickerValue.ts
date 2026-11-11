/**
 * The string contracts of the date and time pickers, in one place. Every
 * value is local wall-clock time with no zone: 'YYYY-MM-DD',
 * 'YYYY-MM-DDTHH:mm' and 'HH:mm', and '' for nothing chosen — the same
 * strings the native date, datetime-local and time inputs used to hand
 * their callers, so a call site swaps the component and keeps its code.
 */

const ISO_DATE = /^(\d{4})-(\d{2})-(\d{2})$/;

const pad = (n: number) => String(n).padStart(2, '0');

/** A 'YYYY-MM-DD' string as a local Date at midnight, or undefined when it
 * is empty or not a real calendar day (2026-02-30). */
export function parseIsoDate(value: string | undefined): Date | undefined {
  const m = ISO_DATE.exec(value ?? '');
  if (!m) return undefined;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const date = new Date(y, mo - 1, d);
  // Date rolls an out-of-range day into the next month rather than refusing it.
  if (date.getFullYear() !== y || date.getMonth() !== mo - 1 || date.getDate() !== d) return undefined;
  return date;
}

/** A Date's local calendar day as 'YYYY-MM-DD'. */
export function toIsoDate(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

const HOUR_MINUTE = /^(\d{1,2})(?:[:.](\d{1,2}))?$/;
const DIGITS = /^(\d{1,2})(\d{2})$/;

/**
 * What someone typed into a time field, as 'HH:mm', or null when it is not
 * a time. Accepts the ways people write one: '9', '9:5', '9:30', '9.30'
 * (German), '930' and '0930'. A one-digit minute after a separator is the
 * minute itself ('9:5' is 09:05), as on a clock.
 */
export function parseTime(text: string): string | null {
  const s = text.trim();
  const m = HOUR_MINUTE.exec(s) ?? DIGITS.exec(s);
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2] ?? 0);
  if (h > 23 || min > 59) return null;
  return `${pad(h)}:${pad(min)}`;
}

/** True for text already in the 'HH:mm' shape — the only shape a time field
 * may commit while it is still being typed in, since every shorter one
 * ('9', '9:3') may yet become something else. */
export function isTimeShape(text: string): boolean {
  return /^\d{2}:\d{2}$/.test(text);
}

export interface DateTimeParts {
  date: string;
  time: string;
}

/** 'YYYY-MM-DDTHH:mm' split into its date and time strings. */
export function splitDateTime(value: string): DateTimeParts {
  const at = value.indexOf('T');
  if (at === -1) return { date: value, time: '' };
  return { date: value.slice(0, at), time: value.slice(at + 1, at + 6) };
}

/** The parts joined back, or '' unless both are there: the value contract
 * has no half-filled date-time. */
export function joinDateTime({ date, time }: DateTimeParts): string {
  return date && time ? `${date}T${time}` : '';
}
