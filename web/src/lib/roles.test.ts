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

  it('gives checks the same bar as agents', () => {
    expect(may('viewer', 'checks.read')).toBe(true);
    expect(may('viewer', 'checks.write')).toBe(false);
    expect(may('operator', 'checks.write')).toBe(true);
    expect(may('admin', 'checks.write')).toBe(true);
  });

  it('lets admins and owners manage the organization', () => {
    expect(may('operator', 'org.manage')).toBe(false);
    expect(may('admin', 'org.manage')).toBe(true);
    expect(may('owner', 'org.manage')).toBe(true);
  });

  it('lets admins and owners manage members', () => {
    expect(may('operator', 'members.manage')).toBe(false);
    expect(may('admin', 'members.manage')).toBe(true);
    expect(may('owner', 'members.manage')).toBe(true);
  });

  it('fails closed for an unknown role or permission', () => {
    expect(may('superuser', 'repos.write')).toBe(false);
    expect(may(undefined, 'repos.write')).toBe(false);
  });
});
