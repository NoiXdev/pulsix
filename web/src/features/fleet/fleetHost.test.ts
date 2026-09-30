import { describe, expect, it } from 'vitest';
import { fleetStateLabel } from './fleetHost.ts';

describe('fleetStateLabel', () => {
  // "no data" is its own word, distinct from HostChecksCard's own
  // "not yet evaluated" for a single tuple: this is a HOST that carries no
  // roll-up row at all, the controller's own ruling that it must never be
  // painted with the colour it last had before its only check was removed.
  it.each([
    ['a host with no roll-up row at all is "no data", never a colour', null, { label: 'no data', tone: 'unknown' }],
    ['ok', 'ok', { label: 'ok', tone: 'ok' }],
    ['warn', 'warn', { label: 'warn', tone: 'warn' }],
    ['crit', 'crit', { label: 'crit', tone: 'crit' }],
    ['unknown', 'unknown', { label: 'unknown', tone: 'unknown' }],
  ] as const)('%s', (_name, state, expected) => {
    expect(fleetStateLabel(state)).toEqual(expected);
  });

  it('gives "no data" and "unknown" different words', () => {
    expect(fleetStateLabel(null).label).not.toBe(fleetStateLabel('unknown').label);
  });
});
