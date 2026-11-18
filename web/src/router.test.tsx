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

  it('has a Secrets page', async () => {
    expect(await settledAt('/o/acme/settings/secrets')).toBe('/o/acme/settings/secrets');
    expect(router.state.matches.some((m) => m.routeId.endsWith('/settings/secrets'))).toBe(true);
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

describe('the job template pages (backlog 7.75)', () => {
  it("has a variables page for a template, beside the list", async () => {
    expect(await settledAt('/o/acme/templates/t1/variables')).toBe('/o/acme/templates/t1/variables');
    expect(router.state.matches.some((m) => m.routeId === '/o/$org/templates/$id/variables')).toBe(true);
  });
});

describe('an invitation link mailed before the token moved into the fragment (backlog 7.80)', () => {
  it('rewrites /invite/{token} to /invite#token={token}', async () => {
    await router.navigate({ to: '/invite/$token', params: { token: 'tok_old' } });
    await router.load();

    expect(router.state.location.pathname).toBe('/invite');
    expect(router.state.location.hash).toBe('token=tok_old');
  });
});

describe('the group page tabs and the links from before them (backlog 7.67)', () => {
  const groupSearch = () => (router.state.matches.at(-1)?.search ?? {}) as { tab?: string };

  it.each([
    ['members', 'members'],
    ['inventory', 'inventory'],
    // The first group page's tab names.
    ['values', undefined],
    ['schema', undefined],
    ['children', 'subgroups'],
    ['preview', 'inventory'],
    ['settings', undefined],
    ['constructor', undefined],
  ])('reads ?tab=%s as the tab %j', async (tab, want) => {
    await router.navigate({ to: '/o/$org/groups/$id', params: { org: 'acme', id: 'g1' }, search: { tab: tab as never } });
    await router.load();

    expect(router.state.location.pathname).toBe('/o/acme/groups/g1');
    expect(groupSearch().tab).toBe(want);
  });

  it.each([
    ['members', 'members'],
    ['children', 'subgroups'],
    ['preview', 'inventory'],
  ])('sends the section anchor #%s on to the tab %s', async (hash, tab) => {
    await router.navigate({ to: '/o/$org/groups/$id', params: { org: 'acme', id: 'g1' }, search: { tab: undefined }, hash });
    await router.load();

    expect(router.state.location.pathname).toBe('/o/acme/groups/g1');
    expect(router.state.location.hash).toBe('');
    expect(groupSearch().tab).toBe(tab);
  });

  it('keeps #schema, which opens the fields of a group just created', async () => {
    await router.navigate({ to: '/o/$org/groups/$id', params: { org: 'acme', id: 'g1' }, search: { tab: undefined }, hash: 'schema' });
    await router.load();

    expect(router.state.location.hash).toBe('schema');
    expect(groupSearch().tab).toBeUndefined();
  });
});

describe('the run request pages', () => {
  it('serves the list under /runs/requests, not as a run id', async () => {
    expect(await settledAt('/o/acme/runs/requests')).toBe('/o/acme/runs/requests');
    expect(router.state.matches.some((m) => m.routeId.endsWith('/runs/requests'))).toBe(true);
  });

  it('serves one request under /runs/requests/$id', async () => {
    expect(await settledAt('/o/acme/runs/requests/rq1')).toBe('/o/acme/runs/requests/rq1');
    expect(router.state.matches.some((m) => m.routeId.endsWith('/runs/requests/$id'))).toBe(true);
  });
});

describe('the server-paged lists', () => {
  it.each(['/o/acme/hosts', '/o/acme/runs'])('keep the sort, order and page of %s in the matched search', async (path) => {
    await router.navigate({ to: path, search: { sort: 'name', order: 'desc', page: 3 } } as never);
    await router.load();
    expect(router.state.matches.at(-1)?.search).toMatchObject({ sort: 'name', order: 'desc', page: 3 });
  });
});

describe('the personal settings area', () => {
  it('sends /account to Security', async () => {
    expect(await settledAt('/account')).toBe('/account/security');
  });

  it.each(['security', 'appearance', 'profile', 'activity'])('has the %s page inside the account layout', async (page) => {
    expect(await settledAt(`/account/${page}`)).toBe(`/account/${page}`);
    expect(router.state.matches.map((m) => m.routeId)).toEqual(['__root__', '/account', `/account/${page}`]);
  });

  it('keeps the sort, order and page of My activity in the URL', async () => {
    await router.navigate({ to: '/account/activity', search: { sort: 'at', order: 'asc', page: 2 } });
    await router.load();
    expect(router.state.location.search).toEqual({ sort: 'at', order: 'asc', page: 2 });
  });

  it.each(['/account/enrol', '/account/password', '/account/lock-pin'])('leaves %s outside the layout, without the shell', async (path) => {
    expect(await settledAt(path)).toBe(path);
    expect(router.state.matches.map((m) => m.routeId)).toEqual(['__root__', path]);
  });
});
