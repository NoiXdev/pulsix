import { describe, expect, it } from 'vitest';
import { formatter } from './format.ts';

// 00:30 UTC is 09:30 in Tokyo (UTC+9, no daylight saving) — and still
// 2 November there, so the date part is checkable too.
const AT = '2026-11-02T00:30:00Z';

describe('formatter (Review Focus 4)', () => {
  it('keeps an explicit time zone in English', () => {
    const out = formatter('en').dateTime(AT, { timeZone: 'Asia/Tokyo', dateStyle: 'medium', timeStyle: 'short' });
    expect(out).toMatch(/Nov 2, 2026/);
    expect(out).toMatch(/9:30\sAM/);
  });

  it('keeps the same explicit time zone in German', () => {
    const out = formatter('de').dateTime(AT, { timeZone: 'Asia/Tokyo', dateStyle: 'medium', timeStyle: 'short' });
    expect(out).toMatch(/02\.11\.2026/);
    expect(out).toMatch(/09:30/);
  });

  it('formats date, time and number for the language', () => {
    const d = new Date(AT);
    expect(formatter('de').date(d, { timeZone: 'UTC' })).toBe('2.11.2026');
    expect(formatter('en').date(d, { timeZone: 'UTC' })).toBe('11/2/2026');
    expect(formatter('de').time(d, { timeZone: 'UTC', hour: '2-digit', minute: '2-digit' })).toBe('00:30');
    expect(formatter('de').number(1234.5)).toBe('1.234,5');
    expect(formatter('en').number(1234.5)).toBe('1,234.5');
  });

  it('says how long ago something was, in the language', () => {
    const now = new Date('2026-11-02T12:00:00Z');
    expect(formatter('en').relative('2026-11-02T11:55:00Z', now)).toBe('5 minutes ago');
    expect(formatter('de').relative('2026-11-02T11:55:00Z', now)).toBe('vor 5 Minuten');
    expect(formatter('de').relative('2026-11-01T12:00:00Z', now)).toBe('gestern');
  });
});
