import { describe, expect, it } from 'vitest';
import { cancelHostRows, hasFailedHosts, hostResultRows, isCancellable, kindLabel, shortId, statusTone } from './status.ts';
import { en } from '../../i18n/en/index.ts';

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

describe('isCancellable', () => {
  it.each([
    ['queued', true],
    ['claimed', true],
    ['running', true],
    ['cancelling', false],
    ['succeeded', false],
    ['failed', false],
    ['cancelled', false],
    ['error', false],
  ])('%s → %s', (status, want) => {
    expect(isCancellable(status)).toBe(want);
  });
});

describe('kindLabel', () => {
  it('labels the known kinds and passes unknown ones through', () => {
    expect(kindLabel('connection_test', en)).toBe('connection test');
    expect(kindLabel('sync_repo', en)).toBe('repo sync');
    expect(kindLabel('revoke_key', en)).toBe('old key removal');
    expect(kindLabel('mystery', en)).toBe('mystery');
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

describe('cancelHostRows', () => {
  const ev = (host: string, task: string, state: string) => ({ host, task, state });

  it('marks a host finished when its last event closes the step', () => {
    const rows = cancelHostRows(['h1'], [ev('h1', 'Install nginx', 'task'), ev('h1', 'Install nginx', 'ok')]);
    expect(rows).toEqual([{ host: 'h1', state: 'finished', task: '' }]);
  });

  it('marks a host interrupted when its last event is task, and names that task', () => {
    const rows = cancelHostRows(['h1'], [ev('h1', 'Install nginx', 'task')]);
    expect(rows).toEqual([{ host: 'h1', state: 'interrupted', task: 'Install nginx' }]);
  });

  it('marks a target no event mentions never-started, with an empty task', () => {
    const rows = cancelHostRows(['h1', 'h2'], []);
    expect(rows).toEqual([
      { host: 'h1', state: 'never-started', task: '' },
      { host: 'h2', state: 'never-started', task: '' },
    ]);
  });

  it('includes a host the events name that targets does not, rather than dropping it', () => {
    const rows = cancelHostRows(['h1'], [ev('h9', 'Restart service', 'task')]);
    expect(rows).toEqual([
      { host: 'h1', state: 'never-started', task: '' },
      { host: 'h9', state: 'interrupted', task: 'Restart service' },
    ]);
  });

  it('ignores events with an empty host', () => {
    const rows = cancelHostRows(['h1'], [ev('', 'Install nginx', 'task')]);
    expect(rows).toEqual([{ host: 'h1', state: 'never-started', task: '' }]);
  });

  it.each(['changed', 'skipped', 'unreachable'])('treats a closing "%s" event as finished', (state) => {
    const rows = cancelHostRows(['h1'], [ev('h1', 'Install nginx', 'task'), ev('h1', 'Install nginx', state)]);
    expect(rows).toEqual([{ host: 'h1', state: 'finished', task: '' }]);
  });

  it('ignores a stdout follow-up event trailing the last task/closing event', () => {
    // The worker emits a `stdout` event as a follow-up to the primary one
    // (events.go); it must not be mistaken for the step's own last state.
    const interrupted = cancelHostRows(['h1'], [ev('h1', 'Install nginx', 'task'), ev('h1', '', 'stdout')]);
    expect(interrupted).toEqual([{ host: 'h1', state: 'interrupted', task: 'Install nginx' }]);
    const finished = cancelHostRows(['h1'], [ev('h1', 'Install nginx', 'task'), ev('h1', 'Install nginx', 'ok'), ev('h1', '', 'stdout')]);
    expect(finished).toEqual([{ host: 'h1', state: 'finished', task: '' }]);
  });

  it('sorts rows by host, localeCompare', () => {
    const rows = cancelHostRows(['h2', 'h1', 'h10'], []);
    expect(rows.map((r) => r.host)).toEqual(['h1', 'h10', 'h2']);
  });
});
