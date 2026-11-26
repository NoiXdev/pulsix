import { describe, expect, it } from 'vitest';
import { pendingPollInterval } from './useTriggers.ts';

describe('pendingPollInterval', () => {
  it('polls quickly while a run waits, slowly while the trigger settles, never otherwise', () => {
    expect(pendingPollInterval(true, true)).toBe(5000);
    expect(pendingPollInterval(false, true)).toBe(5000);
    expect(pendingPollInterval(true, false)).toBe(10000);
    expect(pendingPollInterval(false, false)).toBe(false);
  });
});
