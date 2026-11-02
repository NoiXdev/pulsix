import { describe, expect, it } from 'vitest';
import { fleetStateLabel as label } from './fleetHost.ts';
import { en } from '../../i18n/en/index.ts';

const fleetStateLabel = (state: Parameters<typeof label>[0], allIgnored: boolean) => label(state, allIgnored, en);

describe('fleetStateLabel', () => {
  // "no data" is its own word, distinct from HostChecksCard's own
  // "not yet evaluated" for a single tuple: this is a HOST that carries no
  // roll-up row at all, the controller's own ruling that it must never be
  // painted with the colour it last had before its only check was removed.
  it.each([
    ['a host with no roll-up row at all, nothing ignored, is "no data"', null, false, { label: 'no data', tone: 'unknown' }],
    ['ok', 'ok', false, { label: 'ok', tone: 'ok' }],
    ['warn', 'warn', false, { label: 'warn', tone: 'warn' }],
    ['crit', 'crit', false, { label: 'crit', tone: 'crit' }],
    ['unknown', 'unknown', false, { label: 'unknown', tone: 'unknown' }],
  ] as const)('%s', (_name, state, allIgnored, expected) => {
    expect(fleetStateLabel(state, allIgnored)).toEqual(expected);
  });

  it('gives "no data" and "unknown" different words', () => {
    expect(fleetStateLabel(null, false).label).not.toBe(fleetStateLabel('unknown', false).label);
  });

  // backlog 3.44: a host with no roll-up row because every item it
  // discovered is ignored must read as its own word, not as "no data" —
  // the operator who silenced the host's last item must be able to tell
  // their own decision apart from a host nobody is watching.
  it('reads "all ignored", not "no data", when state is null and allIgnored is set', () => {
    expect(fleetStateLabel(null, true)).toEqual({ label: 'all ignored', tone: 'accent' });
  });

  it('gives "all ignored" and "no data" different words and different tones', () => {
    const allIgnored = fleetStateLabel(null, true);
    const noData = fleetStateLabel(null, false);
    expect(allIgnored.label).not.toBe(noData.label);
    expect(allIgnored.tone).not.toBe(noData.tone);
  });

  // allIgnored is only ever consulted when state is null — the API never
  // sets it alongside a real state, but this pins that even a caller
  // passing a stray true for a judged host would not corrupt its reading.
  it('ignores allIgnored once state is non-null', () => {
    expect(fleetStateLabel('ok', true)).toEqual({ label: 'ok', tone: 'ok' });
  });
});
