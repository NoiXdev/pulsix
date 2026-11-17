import { afterEach, describe, expect, it, vi } from 'vitest';
import { lastOrg, rememberOrg } from './lastOrg.ts';

afterEach(() => {
  vi.restoreAllMocks();
  sessionStorage.clear();
});

describe('lastOrg', () => {
  it('is undefined before an organization was visited', () => {
    expect(lastOrg()).toBeUndefined();
  });

  it('answers the organization remembered last', () => {
    rememberOrg('acme');
    rememberOrg('beta');
    expect(lastOrg()).toBe('beta');
  });

  it('keeps it per tab, in sessionStorage', () => {
    rememberOrg('acme');
    expect(sessionStorage.getItem('pulsix.lastOrg')).toBe('acme');
  });

  it('survives storage that throws', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied');
    });
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied');
    });
    expect(() => rememberOrg('acme')).not.toThrow();
    expect(lastOrg()).toBeUndefined();
  });
});
