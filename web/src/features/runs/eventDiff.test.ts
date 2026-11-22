import { describe, expect, it } from 'vitest';
import { eventDiffs } from './eventDiff.ts';

const withDiff = (diff: unknown) => ({ event: 'runner_on_ok', event_data: { host: 'web-01', task: 'Render', res: { changed: true, diff } } });

describe('eventDiffs', () => {
  it('reads one before/after diff with its headers', () => {
    expect(eventDiffs(withDiff({ before: 'a\n', after: 'b\n', before_header: '/etc/a', after_header: '/etc/a' }))).toEqual([
      { kind: 'compare', before: 'a\n', after: 'b\n', beforeHeader: '/etc/a', afterHeader: '/etc/a' },
    ]);
  });

  it('reads a list of diffs, a prepared one as it is', () => {
    expect(eventDiffs(withDiff([{ prepared: '--- a\n+++ b\n' }, { before: '', after: 'x\n' }]))).toEqual([
      { kind: 'prepared', text: '--- a\n+++ b\n' },
      { kind: 'compare', before: '', after: 'x\n' },
    ]);
  });

  it('writes a structured side as sorted, indented JSON', () => {
    const [d] = eventDiffs(withDiff({ before: { state: 'absent', path: '/srv' }, after: { state: 'directory', path: '/srv' } }));
    expect(d).toEqual({ kind: 'compare', before: '{\n  "path": "/srv",\n  "state": "absent"\n}\n', after: '{\n  "path": "/srv",\n  "state": "directory"\n}\n' });
  });

  it.each([
    ['no event_data', {}],
    ['no res', { event_data: {} }],
    ['no diff', { event_data: { res: { changed: true } } }],
    ['a diff that is a string', withDiff('nope')],
    ['an entry with neither side', withDiff([{ other: 1 }])],
    ['equal sides', withDiff({ before: {}, after: {} })],
    ['a bare stdout line', { text: 'PLAY [all]' }],
  ])('answers nothing for %s', (_name, payload) => {
    expect(eventDiffs(payload)).toEqual([]);
  });
});
