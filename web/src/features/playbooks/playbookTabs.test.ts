import { describe, expect, it } from 'vitest';
import { parsePlaybookTab, validatePlaybookSearch } from './playbookTabs.ts';

describe('playbook tabs', () => {
  it.each([
    [undefined, 'editor'],
    ['editor', 'editor'],
    ['runs', 'runs'],
    ['used', 'used'],
    ['dry', 'editor'],
    [42, 'editor'],
  ])('reads ?tab=%s as %s', (raw, want) => {
    expect(parsePlaybookTab(raw)).toBe(want);
  });

  it('leaves the default tab and unknown ones out of the URL', () => {
    expect(validatePlaybookSearch({ tab: 'runs', other: 1 })).toEqual({ tab: 'runs' });
    expect(validatePlaybookSearch({ tab: 'editor' })).toEqual({ tab: undefined });
    expect(validatePlaybookSearch({ tab: 'nope' })).toEqual({ tab: undefined });
  });
});
