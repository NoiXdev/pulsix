import { describe, expect, it } from 'vitest';
import { areaAllowed, allowedAreas, displayedAreas, toggleArea } from './areas.ts';

describe('assistant token areas', () => {
  it.each([
    ['viewer', 'inventory.read', true],
    ['viewer', 'jobs.read', true],
    ['viewer', 'inventory.write', false],
    ['viewer', 'credentials.names', false],
    ['viewer', 'jobs.request', false],
    ['operator', 'inventory.write', true],
    ['operator', 'credentials.names', true],
    ['operator', 'jobs.request', true],
    ['owner', 'templates.write', true],
    ['nobody', 'inventory.read', false],
  ] as const)('%s may hold %s: %s', (role, area, want) => {
    expect(areaAllowed(role, area)).toBe(want);
  });

  it('ticks the read area with its write area', () => {
    expect(toggleArea([], 'repos.write', true)).toEqual(['repos.read', 'repos.write']);
  });

  it('unticks the write area with its read area', () => {
    expect(toggleArea(['repos.read', 'repos.write', 'jobs.read'], 'repos.read', false)).toEqual(['jobs.read']);
  });

  it('leaves the read area when only the write area is unticked', () => {
    expect(toggleArea(['jobs.read', 'jobs.request'], 'jobs.request', false)).toEqual(['jobs.read']);
  });

  it('drops the areas a role cannot hold', () => {
    expect(allowedAreas(['inventory.read', 'inventory.write', 'credentials.names'], 'viewer')).toEqual(['inventory.read']);
  });

  it('shows a write area without the read area it includes', () => {
    expect(displayedAreas(['inventory.read', 'inventory.write', 'jobs.read', 'credentials.names'])).toEqual([
      'inventory.write',
      'jobs.read',
      'credentials.names',
    ]);
  });
});
