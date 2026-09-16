import { describe, expect, it } from 'vitest';
import { may } from './roles.ts';

describe('may', () => {
  it('mirrors the server matrix', () => {
    expect(may('viewer', 'inventory.write')).toBe(false);
    expect(may('viewer', 'hosts.write')).toBe(false);
    expect(may('operator', 'inventory.write')).toBe(true);
    expect(may('operator', 'repos.write')).toBe(true);
    expect(may('operator', 'jobs.run')).toBe(true);
    expect(may('operator', 'credentials.write')).toBe(false);
    expect(may('admin', 'credentials.write')).toBe(true);
    expect(may('owner', 'credentials.write')).toBe(true);
  });

  it('fails closed for an unknown role or permission', () => {
    expect(may('superuser', 'repos.write')).toBe(false);
    expect(may(undefined, 'repos.write')).toBe(false);
  });
});
