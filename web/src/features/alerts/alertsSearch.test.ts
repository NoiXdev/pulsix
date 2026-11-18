import { describe, expect, it } from 'vitest';
import { validateAlertsSearch } from './alertsSearch.ts';

describe('validateAlertsSearch', () => {
  it('keeps every filter the alerts request takes', () => {
    expect(
      validateAlertsSearch({ state: 'open', severity: 'crit', scope: 'host', acknowledged: false, muted: 'true', host: '00000000-0000-4000-8000-000000000002', sort: 'opened' }),
    ).toEqual({ sort: 'opened', state: 'open', severity: 'crit', scope: 'host', acknowledged: false, muted: true, host: '00000000-0000-4000-8000-000000000002' });
  });

  it('drops values the request does not take', () => {
    const out = validateAlertsSearch({ state: 'ajar', severity: 'meh', scope: 'world', acknowledged: 'maybe', muted: 1, host: '' });
    expect(Object.values(out).filter((v) => v !== undefined)).toEqual([]);
  });
});
