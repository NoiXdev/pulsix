import { describe, expect, it } from 'vitest';
import { usageByField } from './usage.ts';

describe('usageByField', () => {
  const schedules = [{ id: 's1', name: 'nightly', extra_vars: { region: 'eu', token: { $secret: 'set' } } }];
  const triggers = [{ id: 't1', name: 'deploy-hook', mapping: { extra_vars: { region: '$.region' } } }];

  it('lists the schedules, then the webhooks, that override each variable', () => {
    const usage = usageByField(schedules, triggers);
    expect(usage.region).toEqual([
      { kind: 'schedule', id: 's1', name: 'nightly' },
      { kind: 'webhook', id: 't1', name: 'deploy-hook' },
    ]);
    expect(usage.token).toEqual([{ kind: 'schedule', id: 's1', name: 'nightly' }]);
    expect(usage).not.toHaveProperty('port');
  });

  it('copes with schedules and triggers that override nothing', () => {
    expect(usageByField([], [])).toEqual({});
    expect(
      usageByField(
        [{ id: 's', name: 'a' }, { id: 's2', name: 'b', extra_vars: null }],
        [{ id: 't', name: 'c' }, { id: 't2', name: 'd', mapping: null }, { id: 't3', name: 'e', mapping: { extra_vars: null } }],
      ),
    ).toEqual({});
  });

  it('counts a place once per variable, in the order it was given', () => {
    const usage = usageByField(
      [{ id: 'a', name: 'first', extra_vars: { x: 1 } }, { id: 'b', name: 'second', extra_vars: { x: 2 } }],
      [],
    );
    expect(usage.x.map((u) => u.id)).toEqual(['a', 'b']);
  });

  it('takes any variable name, also one that Object has a property for', () => {
    const usage = usageByField([{ id: 'a', name: 'first', extra_vars: { constructor: 1, toString: 2 } }], []);
    expect(usage.constructor).toEqual([{ kind: 'schedule', id: 'a', name: 'first' }]);
    expect(usage.toString).toHaveLength(1);
  });
});
