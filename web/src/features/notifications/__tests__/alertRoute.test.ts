import { describe, expect, it } from 'vitest';
import { compileWindow, decompileWindow, routeReachesNobody, routeToInput } from '../alertRoute.ts';
import type { AlertRoute } from '../../../api/types.ts';

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

// routeReachesNobody predates the channel source: it asked only whether
// the fixed channel list was empty. alertroutes.validateChannelSource now
// REQUIRES a rotation-sourced route to carry no fixed channels, and the
// DTO always emits "channels": [] for one — so every healthy on-call route
// wore the red badge, and the one loud signal the design leans on for a
// rotation that really does resolve to nothing carried no information in
// either direction (whole-branch review, Important #2).
describe('routeReachesNobody', () => {
  const base: AlertRoute = {
    id: 'r1', name: 'Critical to ops', enabled: true, scope: 'org',
    severities: ['crit'], events: ['opened'], channels: ['c1'],
    revision: 1, created_at: '', updated_at: '',
  };

  it('is false for a route with a fixed channel', () => {
    expect(routeReachesNobody(base)).toBe(false);
  });

  it('is true for a route whose last channel was deleted', () => {
    expect(routeReachesNobody({ ...base, channels: [] })).toBe(true);
  });

  it('is false for a rotation-sourced route, which carries no fixed channels by design', () => {
    expect(routeReachesNobody({ ...base, channels: [], rotation_id: 'rot1' })).toBe(false);
    expect(routeReachesNobody({ ...base, channels: [], rotation_id: 'rot1' }, { channels: ['c1', 'c2'] })).toBe(false);
  });

  it('is true for a rotation whose cycle was emptied, when the caller knows the cycle', () => {
    // Deleting a channel cascades it out of every rotation naming it, so a
    // rotation can end up with nobody to hand over to — the case the
    // indicator exists for and the user docs promise it for.
    expect(routeReachesNobody({ ...base, channels: [], rotation_id: 'rot1' }, { channels: [] })).toBe(true);
  });
});

// The PATCH body is a full replace (AlertRouteBody's own doc comment), so
// anything left out of it is deleted. routeToInput is the one place that
// turns a stored route back into that body — the list's enable/disable
// toggle rebuilt it field by field and silently dropped the window, the
// breakthrough severities and the rotation (whole-branch review,
// Important #3).
describe('routeToInput', () => {
  it('carries the window, the breakthrough severities and the rotation', () => {
    expect(
      routeToInput({
        id: 'r1', name: 'Quiet hours', enabled: true, scope: 'host', scope_id: 'h1',
        severities: ['crit', 'warn'], events: ['opened'], channels: [],
        window: { cron: '0 9 * * 1,2,3,4,5', duration_seconds: 28800, timezone: 'Europe/Berlin' },
        breakthrough_severities: ['crit'], rotation_id: 'rot1',
        revision: 4, created_at: '', updated_at: '',
      }),
    ).toEqual({
      name: 'Quiet hours', enabled: true, scope: 'host', scope_id: 'h1',
      severities: ['crit', 'warn'], events: ['opened'],
      window: { cron: '0 9 * * 1,2,3,4,5', duration_seconds: 28800, timezone: 'Europe/Berlin' },
      breakthrough_severities: ['crit'], rotation_id: 'rot1',
    });
  });

  it('sends a fixed-channel route its channels and no rotation', () => {
    expect(
      routeToInput({
        id: 'r1', name: 'Critical to ops', enabled: false, scope: 'org',
        severities: ['crit'], events: ['opened'], channels: ['c1', 'c2'],
        revision: 1, created_at: '', updated_at: '',
      }),
    ).toEqual({ name: 'Critical to ops', enabled: false, scope: 'org', severities: ['crit'], events: ['opened'], channels: ['c1', 'c2'] });
  });
});
