export type SimpleKind = 'every_minutes' | 'every_hours' | 'daily' | 'weekly' | 'monthly';

export interface SimpleForm {
  kind: SimpleKind;
  every: number;
  time: string;
  days: string[];
  day: number;
}

export const WEEKDAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const;
export const MINUTE_STEPS = [1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30];
export const HOUR_STEPS = [1, 2, 3, 4, 6, 8, 12];

export const DEFAULT_SIMPLE: SimpleForm = { kind: 'daily', every: 15, time: '03:00', days: ['mon'], day: 1 };

/** toSimpleBody is the API's simple object: exactly the fields the kind
 * compiles from (internal/cronexpr Simple.Compile), days in week order. */
export function toSimpleBody(f: SimpleForm) {
  switch (f.kind) {
    case 'every_minutes':
    case 'every_hours':
      return { kind: f.kind, every: f.every };
    case 'daily':
      return { kind: f.kind, time: f.time };
    case 'weekly':
      return { kind: f.kind, days: WEEKDAYS.filter((d) => f.days.includes(d)), time: f.time };
    case 'monthly':
      return { kind: f.kind, day: f.day, time: f.time };
  }
}

export function fromSimpleBody(s: { kind: string; every?: number; time?: string; days?: string[] | null; day?: number } | undefined | null): SimpleForm {
  if (!s) return DEFAULT_SIMPLE;
  return {
    kind: s.kind as SimpleKind,
    every: s.every ?? DEFAULT_SIMPLE.every,
    time: s.time ?? DEFAULT_SIMPLE.time,
    days: s.days ?? DEFAULT_SIMPLE.days,
    day: s.day ?? DEFAULT_SIMPLE.day,
  };
}

/** relative renders a future instant the way the mockup's "Next" column
 * does: minutes under an hour, hours under two days, then days. */
export function relative(at: string | undefined | null, now: Date): string {
  if (!at) return '—';
  const mins = Math.round((new Date(at).getTime() - now.getTime()) / 60000);
  if (mins < 60) return `in ${Math.max(mins, 0)} min`;
  const hours = Math.round(mins / 60);
  if (hours < 48) return `in ${hours} h`;
  return `in ${Math.round(hours / 24)} d`;
}
