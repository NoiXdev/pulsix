import { describe, expect, it } from 'vitest';
import type { TemplateVariablesSource } from '../../../api/types.ts';
import { de } from '../../../i18n/de/index.ts';
import { en } from '../../../i18n/en/index.ts';
import { sourceNotices, unavailableReason } from './sourceNotices.ts';

const s = en.templates.source;

function source(over: Partial<TemplateVariablesSource>): TemplateVariablesSource {
  return {
    applied_rev: '',
    available: true,
    commit: 'abc',
    found: true,
    incomplete: false,
    kind: 'repo',
    left_out: false,
    newer: false,
    worker_reads: true,
    ...over,
  };
}

describe('sourceNotices', () => {
  it('describes a repository playbook a sync left out as a sync problem', () => {
    const src = source({ available: false, incomplete: true, left_out: true, synced_at: '2026-11-10T00:00:00Z' });
    expect(sourceNotices(src, s)).toEqual([s.incomplete, s.leftOut]);
    expect(unavailableReason(src, s)).toBe(s.leftOut);
  });

  // An editor playbook has no sync: its read is left out because its
  // variables are over the 1 MiB cap, and the fix is to shorten it and save.
  it('tells an editor playbook that was left out to shorten it and save', () => {
    const src = source({ kind: 'inline', commit: '', available: false, incomplete: true, left_out: true, worker_reads: true });
    expect(sourceNotices(src, s)).toEqual([s.inlineRoles, s.inlineLeftOut]);
    expect(unavailableReason(src, s)).toBe(s.inlineLeftOut);
    expect(s.inlineLeftOut).not.toMatch(/sync/i);
  });

  it('tells an incomplete editor playbook to shorten it and save', () => {
    const src = source({ kind: 'inline', commit: 'text:abc', incomplete: true });
    expect(sourceNotices(src, s)).toEqual([s.inlineRoles, s.inlineIncomplete]);
    expect(s.inlineIncomplete).not.toMatch(/sync|repository/i);
  });

  it('names the cap and the fix in both languages', () => {
    expect(s.inlineLeftOut).toBe("This playbook's variables are too large to read (over 1 MiB); shorten it and save.");
    const d = de.templates.source;
    for (const text of [d.inlineLeftOut, d.inlineIncomplete]) {
      expect(text).toMatch(/1 MiB/);
      expect(text).not.toMatch(/Abgleich|Repository/);
    }
    expect(d.inlineLeftOut).not.toBe(s.inlineLeftOut);
  });
});
