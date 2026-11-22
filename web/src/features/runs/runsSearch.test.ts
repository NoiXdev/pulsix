import { describe, expect, it } from 'vitest';
import { liveRunsOf, validateRunsSearch } from './runsSearch.ts';

describe('validateRunsSearch', () => {
  it('keeps the offered values and the list keys', () => {
    expect(validateRunsSearch({ status: 'active', kind: 'bootstrap', trigger: 'schedule', schedule: '00000000-0000-4000-8000-000000000006', host: '00000000-0000-4000-8000-000000000002', page: 2 })).toEqual({
      page: 2,
      status: 'active',
      kind: 'bootstrap',
      trigger: 'schedule',
      schedule: '00000000-0000-4000-8000-000000000006',
      host: '00000000-0000-4000-8000-000000000002',
    });
  });

  it('drops values the list does not offer', () => {
    const out = validateRunsSearch({ status: 'exploded', kind: 'nope', trigger: 'cron', schedule: '', host: 7 });
    expect(out).toMatchObject({ status: undefined, kind: undefined, trigger: undefined, schedule: undefined, host: undefined });
  });

  it('reads the dry runs filter as check_mode, yes or no', () => {
    expect(validateRunsSearch({ check_mode: 'true' })).toMatchObject({ check_mode: true });
    expect(validateRunsSearch({ check_mode: true })).toMatchObject({ check_mode: true });
    expect(validateRunsSearch({ check_mode: 'false' })).toMatchObject({ check_mode: false });
    expect(validateRunsSearch({ check_mode: 'maybe' })).toMatchObject({ check_mode: undefined });
  });

  it('reads back the live runs link of one host', () => {
    expect(validateRunsSearch(liveRunsOf('00000000-0000-4000-8000-000000000002'))).toMatchObject({ host: '00000000-0000-4000-8000-000000000002', status: 'active' });
  });
});
