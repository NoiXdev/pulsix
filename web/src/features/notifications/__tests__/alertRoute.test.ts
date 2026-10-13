import { describe, expect, it } from 'vitest';
import { compileWindow, decompileWindow } from '../alertRoute.ts';

// compileWindow is the piece most likely to be quietly wrong (task brief):
// weekday checkboxes plus a from/until pair compiled into the
// {cron, duration_seconds, timezone} triple alertroutes.Window stores
// (internal/alertroutes/window.go). The cron names the instant the window
// OPENS — so its hour and minute always come from `from`, never `until` —
// and the duration carries it to `until`, wrapping through midnight when
// `until` is not strictly after `from`.
describe('compileWindow', () => {
  it('compiles an ordinary same-day window', () => {
    expect(compileWindow({ days: ['mon', 'tue', 'wed', 'thu', 'fri'], from: '09:00', until: '17:00', timezone: 'Europe/Berlin' })).toEqual({
      cron: '0 9 * * 1,2,3,4,5',
      duration_seconds: 8 * 3600,
      timezone: 'Europe/Berlin',
    });
  });

  it('wraps the duration when until is earlier than from (quiet hours, 22:00 to 07:00 is nine hours)', () => {
    expect(compileWindow({ days: ['mon'], from: '22:00', until: '07:00', timezone: 'UTC' })).toEqual({
      cron: '0 22 * * 1',
      duration_seconds: 9 * 3600,
      timezone: 'UTC',
    });
  });

  it('does not mistake a wrapped window for a negative duration', () => {
    const { duration_seconds: seconds } = compileWindow({ days: ['mon'], from: '22:00', until: '07:00', timezone: 'UTC' });
    expect(seconds).toBeGreaterThan(0);
  });

  it('treats from === until as the whole day rather than a zero-length window', () => {
    // Decision: a window that opens and closes at the same instant would
    // never actually deliver anything, which is never what leaving the
    // two fields alone was for — so this reads as "all day", not "never".
    expect(compileWindow({ days: ['mon'], from: '09:00', until: '09:00', timezone: 'UTC' })).toEqual({
      cron: '0 9 * * 1',
      duration_seconds: 24 * 3600,
      timezone: 'UTC',
    });
  });

  it('collapses every weekday selected to a star', () => {
    const { cron } = compileWindow({
      days: ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'],
      from: '00:00',
      until: '00:00',
      timezone: 'UTC',
    });
    expect(cron).toBe('0 0 * * *');
  });

  it('defaults to every day when no weekday is selected', () => {
    const { cron } = compileWindow({ days: [], from: '01:30', until: '02:00', timezone: 'UTC' });
    expect(cron).toBe('30 1 * * *');
  });

  it('sorts and de-duplicates the weekday list', () => {
    const { cron } = compileWindow({ days: ['fri', 'mon', 'mon'], from: '09:00', until: '10:00', timezone: 'UTC' });
    expect(cron).toBe('0 9 * * 1,5');
  });
});

describe('decompileWindow', () => {
  it('inverts a same-day compiled window', () => {
    const compiled = compileWindow({ days: ['mon', 'wed'], from: '09:00', until: '17:30', timezone: 'Europe/Berlin' });
    expect(decompileWindow(compiled)).toEqual({ days: ['mon', 'wed'], from: '09:00', until: '17:30', timezone: 'Europe/Berlin' });
  });

  it('inverts a midnight-crossing window', () => {
    const compiled = compileWindow({ days: ['sun'], from: '22:00', until: '07:00', timezone: 'UTC' });
    expect(decompileWindow(compiled)).toEqual({ days: ['sun'], from: '22:00', until: '07:00', timezone: 'UTC' });
  });

  it('reads a full-day window back as from === until', () => {
    const compiled = compileWindow({ days: ['mon'], from: '09:00', until: '09:00', timezone: 'UTC' });
    const draft = decompileWindow(compiled);
    expect(draft.from).toBe('09:00');
    expect(draft.until).toBe('09:00');
  });

  it('reads a star day-of-week as all seven days', () => {
    expect(decompileWindow({ cron: '0 0 * * *', duration_seconds: 3600, timezone: 'UTC' }).days).toEqual([
      'mon',
      'tue',
      'wed',
      'thu',
      'fri',
      'sat',
      'sun',
    ]);
  });
});
