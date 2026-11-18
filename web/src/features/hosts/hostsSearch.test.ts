import { describe, expect, it } from 'vitest';
import { validateHostsSearch } from './hostsSearch.ts';

describe('validateHostsSearch zone', () => {
  it('keeps a UUID-shaped zone id', () => {
    const id = '3f2b8c1e-7a4d-4e6b-9c0a-1d2e3f4a5b6c';
    expect(validateHostsSearch({ zone: id }).zone).toBe(id);
  });

  it.each(['fra', 'z1', 42, '', '3f2b8c1e', null, {}])('drops a hand-typed %j to no zone filter', (zone) => {
    expect(validateHostsSearch({ zone }).zone).toBeUndefined();
  });
});
