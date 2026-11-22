import { describe, expect, it } from 'vitest';
import { PLAYBOOK_TABS, parsePlaybookTab, validatePlaybookSearch } from './playbookTabs.ts';

describe('playbook tabs', () => {
  it.each([
    [undefined, 'editor'],
    ['editor', 'editor'],
    ['dry-run', 'dry-run'],
    ['runs', 'runs'],
    ['used', 'used'],
    ['dry', 'editor'],
    [42, 'editor'],
  ])('reads ?tab=%s as %s', (raw, want) => {
    expect(parsePlaybookTab(raw)).toBe(want);
  });

  it('orders the tabs Editor, Dry run, Runs, Used by', () => {
    expect(PLAYBOOK_TABS).toEqual(['editor', 'dry-run', 'runs', 'used']);
  });

  it('leaves the default tab and unknown ones out of the URL', () => {
    expect(validatePlaybookSearch({ tab: 'runs', other: 1 })).toEqual({ tab: 'runs' });
    expect(validatePlaybookSearch({ tab: 'editor' })).toEqual({ tab: undefined });
    expect(validatePlaybookSearch({ tab: 'nope' })).toEqual({ tab: undefined });
  });
});
