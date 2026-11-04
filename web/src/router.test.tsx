import { beforeAll, describe, expect, it } from 'vitest';
import { router } from './router.tsx';

/** Navigates the real application router and reports where it settled.
 * Navigation runs the beforeLoad chain — where the redirects live —
 * without rendering any page, so no API has to be mocked. */
async function settledAt(path: string): Promise<string> {
  await router.navigate({ to: path, params: { org: path.split('/')[2] } });
  await router.load();
  return router.state.location.pathname;
}

/**
 * Credentials and Jump hosts moved out of the Settings tab strip into
 * the sidebar's Infrastructure group (v0.4f), and their URLs moved with
 * them. The old paths stay reachable: they are in bookmarks, in older
 * plan documents, and in anything a user pasted to a colleague. Landing
 * on one must not show a Settings page whose tab strip no longer
 * contains it.
 */
describe('the Settings paths Credentials and Jump hosts left behind', () => {
  beforeAll(async () => {
    await router.load();
  });

  it('redirects /settings/credentials to the top-level Credentials page', async () => {
    expect(await settledAt('/o/acme/settings/credentials')).toBe('/o/acme/credentials');
  });

  it('redirects /settings/jump-hosts to the top-level Jump hosts page', async () => {
    expect(await settledAt('/o/acme/settings/jump-hosts')).toBe('/o/acme/jump-hosts');
  });

  it('carries the organization slug through rather than hard-coding one', async () => {
    expect(await settledAt('/o/hosts-inc/settings/credentials')).toBe('/o/hosts-inc/credentials');
  });

  it('leaves the Settings paths that did not move alone', async () => {
    expect(await settledAt('/o/acme/settings/tokens')).toBe('/o/acme/settings/tokens');
  });
});

describe('the Settings section', () => {
  it('still sends /settings to Members', async () => {
    expect(await settledAt('/o/acme/settings')).toBe('/o/acme/settings/members');
  });

  it('has a General page', async () => {
    expect(await settledAt('/o/acme/settings/general')).toBe('/o/acme/settings/general');
    expect(router.state.matches.some((m) => m.routeId.endsWith('/settings/general'))).toBe(true);
  });
});

describe('the shared resources pages', () => {
  it('sends /settings/shared to the repositories list', async () => {
    expect(await settledAt('/o/acme/settings/shared')).toBe('/o/acme/settings/shared/repos');
  });

  it('sends the former /settings/shared-resources page to the repositories list', async () => {
    expect(await settledAt('/o/acme/settings/shared-resources')).toBe('/o/acme/settings/shared/repos');
  });

  it('carries the organization slug through the redirect', async () => {
    expect(await settledAt('/o/hosts-inc/settings/shared')).toBe('/o/hosts-inc/settings/shared/repos');
  });

  it.each(['repos', 'templates', 'checks'])('has a list, a new and an edit page for %s', async (kind) => {
    for (const tail of ['', '/new', '/abc']) {
      const path = `/o/acme/settings/shared/${kind}${tail}`;
      expect(await settledAt(path)).toBe(path);
    }
  });
});

describe('the password pages (backlog 7.74)', () => {
  it.each(['/forgot-password', '/reset-password', '/account/password'])('routes %s to a page of its own', async (path) => {
    expect(await settledAt(path)).toBe(path);
    expect(router.state.matches.some((m) => m.routeId === path)).toBe(true);
  });
});
