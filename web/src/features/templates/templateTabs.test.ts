import { describe, expect, it } from 'vitest';
import { parseTemplateTab, templateTabId, validateTemplateSearch, TEMPLATE_PANEL_ID } from './templateTabs.ts';

describe('parseTemplateTab', () => {
  it.each([
    { raw: 'overview', want: 'overview' },
    { raw: 'variables', want: 'variables' },
    { raw: 'schedules', want: 'schedules' },
    { raw: 'triggers', want: 'triggers' },
    { raw: 'runs', want: 'runs' },
    { raw: 'settings', want: 'settings' },
    { raw: undefined, want: 'overview' },
    { raw: 'bogus', want: 'overview' },
    { raw: 'toString', want: 'overview' },
    { raw: 7, want: 'overview' },
  ])('$raw reads as $want', ({ raw, want }) => {
    expect(parseTemplateTab(raw)).toBe(want);
  });
});

describe('validateTemplateSearch', () => {
  it('keeps a known tab', () => {
    expect(validateTemplateSearch({ tab: 'settings' })).toEqual({ tab: 'settings' });
  });

  it('leaves the default and anything unknown out of the URL', () => {
    expect(validateTemplateSearch({ tab: 'overview' })).toEqual({ tab: undefined });
    expect(validateTemplateSearch({ tab: 'nope' })).toEqual({ tab: undefined });
    expect(validateTemplateSearch({})).toEqual({ tab: undefined });
  });
});

describe('tab ids', () => {
  it('names each tab button and the one panel', () => {
    expect(templateTabId('runs')).toBe('template-tab-runs');
    expect(TEMPLATE_PANEL_ID).toBe('template-tab-panel');
  });
});
