import { describe, expect, it } from 'vitest';
import type { Job } from '../../api/types.ts';
import { runSource } from './templateRuns.ts';

const schedules = [{ id: 's1', name: 'nightly' }];
const triggers = [{ id: 'w1', name: 'push-main' }];

describe('runSource', () => {
  it.each<[string, Pick<Job, 'trigger' | 'trigger_ref'>, string | undefined]>([
    ['a schedule by its name', { trigger: 'schedule', trigger_ref: 's1' }, 'nightly'],
    ['a webhook trigger by its name', { trigger: 'webhook', trigger_ref: 'w1' }, 'push-main'],
    ['nothing for a manual run', { trigger: 'manual' }, undefined],
    ['nothing for a chained run', { trigger: 'chain', trigger_ref: 's1' }, undefined],
    ['nothing for a schedule that is gone', { trigger: 'schedule', trigger_ref: 'gone' }, undefined],
    ['no schedule for a webhook id', { trigger: 'webhook', trigger_ref: 's1' }, undefined],
    ['nothing without a reference', { trigger: 'schedule' }, undefined],
  ])('names %s', (_, job, want) => {
    expect(runSource(job, schedules, triggers)).toBe(want);
  });

  it('names nothing while the lists are read', () => {
    expect(runSource({ trigger: 'schedule', trigger_ref: 's1' }, undefined, undefined)).toBeUndefined();
  });
});
