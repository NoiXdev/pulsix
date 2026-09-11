import { describe, expect, it } from 'vitest';
import { crumbLabels } from './breadcrumbs.ts';

describe('crumbLabels', () => {
  it('labels a single top-level segment', () => {
    expect(crumbLabels('/o/acme/hosts', 'acme')).toEqual(['Hosts']);
  });

  it('labels a nested Settings route as Settings > Members', () => {
    expect(crumbLabels('/o/acme/settings/members', 'acme')).toEqual(['Settings', 'Members']);
  });

  it('labels every settings leaf', () => {
    expect(crumbLabels('/o/acme/settings/activity', 'acme')).toEqual(['Settings', 'Activity']);
    expect(crumbLabels('/o/acme/settings/tokens', 'acme')).toEqual(['Settings', 'Tokens']);
    expect(crumbLabels('/o/acme/settings/mail', 'acme')).toEqual(['Settings', 'Outgoing mail']);
  });

  it('shortens a UUID-shaped host id segment it has no label for', () => {
    expect(crumbLabels('/o/acme/hosts/550e8400-e29b-41d4-a716-446655440000', 'acme')).toEqual([
      'Hosts',
      '550e8400',
    ]);
  });

  it('shows a non-UUID unknown segment verbatim', () => {
    expect(crumbLabels('/o/acme/hosts/web-04', 'acme')).toEqual(['Hosts', 'web-04']);
  });

  it('returns no crumbs for a path outside this org', () => {
    expect(crumbLabels('/account/security', 'acme')).toEqual([]);
    expect(crumbLabels('/o/other-org/hosts', 'acme')).toEqual([]);
  });

  it('does not confuse an org slug that is a prefix of another org slug', () => {
    // finding "Important 11"'s own regression class (pathname.includes
    // matching more than intended) — crumbLabels must key off the exact
    // "/o/{org}/" prefix, not a loose substring.
    expect(crumbLabels('/o/acme-staging/hosts', 'acme')).toEqual([]);
  });
});
