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
