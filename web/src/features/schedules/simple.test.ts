import { describe, expect, it } from 'vitest';
import { DEFAULT_SIMPLE, fromSimpleBody, relative, toSimpleBody } from './simple.ts';

describe('simple mode form', () => {
  it('sends only the fields its kind uses', () => {
    expect(toSimpleBody({ ...DEFAULT_SIMPLE, kind: 'every_minutes', every: 15 })).toEqual({ kind: 'every_minutes', every: 15 });
    expect(toSimpleBody({ ...DEFAULT_SIMPLE, kind: 'weekly', days: ['thu', 'tue'], time: '03:00' })).toEqual({
      kind: 'weekly', days: ['tue', 'thu'], time: '03:00',
    });
    expect(toSimpleBody({ ...DEFAULT_SIMPLE, kind: 'monthly', day: 1, time: '04:00' })).toEqual({ kind: 'monthly', day: 1, time: '04:00' });
  });

  it('round-trips a stored body and defaults the rest', () => {
    const f = fromSimpleBody({ kind: 'daily', time: '02:30' });
    expect(f.kind).toBe('daily');
    expect(f.time).toBe('02:30');
    expect(f.days).toEqual(DEFAULT_SIMPLE.days);
    expect(fromSimpleBody(null)).toEqual(DEFAULT_SIMPLE);
  });

  it('renders relative next-run times', () => {
    const now = new Date('2026-09-18T10:00:00Z');
    expect(relative('2026-09-18T10:07:00Z', now)).toBe('in 7 min');
    expect(relative('2026-09-19T00:00:00Z', now)).toBe('in 14 h');
    expect(relative('2026-10-08T10:00:00Z', now)).toBe('in 20 d');
    expect(relative(null, now)).toBe('—');
  });
});
