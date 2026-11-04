import { describe, expect, it } from 'vitest';
import { crumbLabels as crumbLabelsFor } from './breadcrumbs.ts';
import { en } from '../i18n/en/index.ts';
import { de } from '../i18n/de/index.ts';

const crumbLabels = (pathname: string, orgSlug: string) => crumbLabelsFor(pathname, orgSlug, en);

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
    expect(crumbLabels('/o/acme/settings/language', 'acme')).toEqual(['Settings', 'Language']);
  });

  it('labels the inventory groups and credentials/jump-hosts segments', () => {
    expect(crumbLabels('/o/acme/groups', 'acme')).toEqual(['Groups']);
    expect(crumbLabels('/o/acme/credentials', 'acme')).toEqual(['Credentials']);
    expect(crumbLabels('/o/acme/jump-hosts', 'acme')).toEqual(['Jump hosts']);
  });

  it('labels the checks section and its nested monitoring groups page', () => {
    expect(crumbLabels('/o/acme/checks', 'acme')).toEqual(['Checks']);
    expect(crumbLabels('/o/acme/checks/monitoring-groups', 'acme')).toEqual(['Checks', 'Monitoring groups']);
  });

  it('shortens a UUID-shaped host id segment it has no label for', () => {
    expect(crumbLabels('/o/acme/hosts/550e8400-e29b-41d4-a716-446655440000', 'acme')).toEqual([
      'Hosts',
      '550e8400',
    ]);
  });

  it('names a member or account id by what it is, not by the raw id', () => {
    const id = '550e8400-e29b-41d4-a716-446655440000';
    expect(crumbLabels(`/o/acme/settings/members/${id}`, 'acme')).toEqual(['Settings', 'Members', 'Member']);
    expect(crumbLabels(`/o/acme/settings/users/${id}`, 'acme')).toEqual(['Settings', 'Users', 'Account']);
    expect(crumbLabelsFor(`/o/acme/settings/users/${id}`, 'acme', de)).toEqual(['Einstellungen', 'Benutzer', 'Konto']);
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

  it('labels in German with the German catalogue', () => {
    expect(crumbLabelsFor('/o/acme/settings/language', 'acme', de)).toEqual(['Einstellungen', 'Sprache']);
  });

  it('does not take an inherited property name for a label', () => {
    expect(crumbLabels('/o/acme/hosts/constructor', 'acme')).toEqual(['Hosts', 'constructor']);
  });
});
