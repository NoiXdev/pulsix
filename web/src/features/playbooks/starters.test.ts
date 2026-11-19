import { describe, expect, it } from 'vitest';
import { en } from '../../i18n/en/index.ts';
import { PLAYBOOK_STARTERS } from './starters.ts';
import { validateYAMLProblem } from './validate.ts';

describe('PLAYBOOK_STARTERS', () => {
  it('offers the four starters in order', () => {
    expect(PLAYBOOK_STARTERS.map((s) => s.id)).toEqual(['empty', 'packages', 'template', 'service']);
  });

  it.each(PLAYBOOK_STARTERS)('$id passes the preview and uses fully qualified modules', ({ yaml }) => {
    expect(validateYAMLProblem(yaml, en)).toBeNull();
    expect(yaml.endsWith('\n')).toBe(true);
    for (const m of yaml.matchAll(/^\s+(?:- )?([a-z_]+\.[a-z_.]+):/gm)) expect(m[1]).toMatch(/^ansible\.builtin\.[a-z_]+$/);
    expect(yaml).not.toMatch(/^\s+(?:- )?(?:debug|package|apt|dnf|template|copy|service|service_facts|assert):/m);
  });
});
