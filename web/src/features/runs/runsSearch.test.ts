import { describe, expect, it } from 'vitest';
import { liveRunsOf, validateRunsSearch } from './runsSearch.ts';

describe('validateRunsSearch', () => {
  it('keeps the offered values and the list keys', () => {
    expect(validateRunsSearch({ status: 'active', kind: 'bootstrap', trigger: 'schedule', schedule: 's1', host: 'h1', page: 2 })).toEqual({
      page: 2,
      status: 'active',
      kind: 'bootstrap',
      trigger: 'schedule',
      schedule: 's1',
      host: 'h1',
    });
  });

  it('drops values the list does not offer', () => {
    const out = validateRunsSearch({ status: 'exploded', kind: 'nope', trigger: 'cron', schedule: '', host: 7 });
    expect(out).toMatchObject({ status: undefined, kind: undefined, trigger: undefined, schedule: undefined, host: '7' });
  });

  it('reads back the live runs link of one host', () => {
    expect(validateRunsSearch(liveRunsOf('h1'))).toMatchObject({ host: 'h1', status: 'active' });
  });
});
