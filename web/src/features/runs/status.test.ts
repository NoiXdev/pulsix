import { describe, expect, it } from 'vitest';
import { hasFailedHosts, hostResultRows, kindLabel, shortId, statusTone } from './status.ts';

describe('statusTone', () => {
  it('maps every job status onto the state colours', () => {
    expect(statusTone('succeeded')).toBe('ok');
    expect(statusTone('running')).toBe('accent');
    expect(statusTone('cancelling')).toBe('warn');
    expect(statusTone('failed')).toBe('crit');
    expect(statusTone('error')).toBe('crit');
    expect(statusTone('queued')).toBe('unknown');
    expect(statusTone('claimed')).toBe('unknown');
    expect(statusTone('cancelled')).toBe('unknown');
  });
});

describe('kindLabel', () => {
  it('labels the known kinds and passes unknown ones through', () => {
    expect(kindLabel('connection_test')).toBe('connection test');
    expect(kindLabel('sync_repo')).toBe('repo sync');
    expect(kindLabel('revoke_key')).toBe('old key removal');
    expect(kindLabel('mystery')).toBe('mystery');
  });
});

describe('shortId', () => {
  it('keeps the first block of a uuid', () => {
    expect(shortId('0197c0a2-1b2c-7abc-def0-1234567890ab')).toBe('0197c0a2');
  });
});

describe('hostResultRows', () => {
  it('folds the five host lists into one row per host', () => {
    const rows = hostResultRows({
      ok_hosts: ['h1', 'h2'],
      changed_hosts: ['h2'],
      failed_hosts: ['h3'],
      unreachable_hosts: ['h4'],
      skipped_hosts: ['h1'],
    });
    expect(rows).toEqual([
      { host: 'h1', ok: true, changed: false, failed: false, unreachable: false, skipped: true },
      { host: 'h2', ok: true, changed: true, failed: false, unreachable: false, skipped: false },
      { host: 'h3', ok: false, changed: false, failed: true, unreachable: false, skipped: false },
      { host: 'h4', ok: false, changed: false, failed: false, unreachable: true, skipped: false },
    ]);
  });

  it('ignores non-string entries and empty stats', () => {
    expect(hostResultRows({ ok_hosts: [1, 'h1', null] })).toEqual([
      { host: 'h1', ok: true, changed: false, failed: false, unreachable: false, skipped: false },
    ]);
    expect(hostResultRows(undefined)).toEqual([]);
    expect(hostResultRows({ failed_hosts: 'not-a-list' })).toEqual([]);
  });
});

describe('hasFailedHosts', () => {
  it('is true for failed and unreachable hosts only', () => {
    expect(hasFailedHosts({ failed_hosts: ['h1'] })).toBe(true);
    expect(hasFailedHosts({ unreachable_hosts: ['h1'] })).toBe(true);
    expect(hasFailedHosts({ ok_hosts: ['h1'] })).toBe(false);
    expect(hasFailedHosts({})).toBe(false);
  });
});
