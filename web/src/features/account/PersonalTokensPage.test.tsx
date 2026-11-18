import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, numbered, sortBy } from '../../test/wholeList.ts';
import { PersonalTokensPage } from './PersonalTokensPage.tsx';
import { validatePersonalTokensSearch } from './personalTokensSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const ORGS = {
  orgs: [
    { id: 'o1', slug: 'acme', name: 'Acme', role: 'operator', created_at: '2026-01-01T00:00:00Z' },
    { id: 'o2', slug: 'beta', name: 'Beta', role: 'admin', created_at: '2026-01-01T00:00:00Z' },
    { id: 'o3', slug: 'gamma', name: 'Gamma', role: 'owner', created_at: '2026-01-01T00:00:00Z' },
    { id: 'o4', slug: 'delta', name: 'Delta', role: 'admin', created_at: '2026-01-01T00:00:00Z' },
  ],
};
const ORG_IDS: Record<string, string> = { acme: 'o1', beta: 'o2', gamma: 'o3', delta: 'o4' };

const grant = (slug: string, role: string, state = 'active') => ({
  org_id: ORG_IDS[slug],
  org_slug: slug,
  org_name: slug[0].toUpperCase() + slug.slice(1),
  role,
  state,
});
const token = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`,
  name,
  kind: 'api',
  created_at: '2026-01-01T00:00:00Z',
  grants: [grant('acme', 'viewer')],
  ...over,
});

interface Setup {
  tokens?: unknown[];
  /** Organizations whose switch "Allow personal tokens" is off. */
  off?: string[];
  onPost?: (req: MockRequest) => unknown;
  onPatch?: (req: MockRequest) => unknown;
  onDelete?: (req: MockRequest) => void;
  locale?: 'de';
  initial?: string;
}

function setup(opts: Setup = {}) {
  const routes: Record<string, unknown> = {
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs': ORGS,
    'GET /api/v1/auth/me/tokens': { tokens: opts.tokens ?? [] },
    'POST /api/v1/auth/me/tokens': (req: MockRequest) =>
      opts.onPost?.(req) ?? { ...token('new'), token: 'pxp_secret123', name: (req.body as { name: string }).name },
  };
  for (const slug of Object.keys(ORG_IDS)) {
    routes[`GET /api/v1/orgs/${slug}/settings/personal-tokens`] = { allowed: !(opts.off ?? []).includes(slug) };
  }
  for (const t of (opts.tokens ?? []) as Array<{ id: string }>) {
    routes[`PATCH /api/v1/auth/me/tokens/${t.id}`] = (req: MockRequest) => opts.onPatch?.(req) ?? { token: t };
    routes[`DELETE /api/v1/auth/me/tokens/${t.id}`] = (req: MockRequest) => {
      opts.onDelete?.(req);
      return {};
    };
  }
  mockApi(routes);
  return renderListPage({ path: '/tokens', component: PersonalTokensPage, initial: opts.initial ?? '/o/acme/tokens', locale: opts.locale, extraSearch: (q) => ({ ...validatePersonalTokensSearch(q) }) });
}

/** The grants editor's row for one organization. */
function grantRow(org: string): HTMLElement {
  return screen.getByRole('checkbox', { name: `Use in ${org}` }).closest('[data-grant-row]') as HTMLElement;
}

async function openCreate(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Create token' }));
  return screen.findByRole('dialog', { name: 'Create token' });
}

describe('PersonalTokensPage', () => {
  it('opens the create dialog for a person without any organization and says so instead of offering grants', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/orgs': { orgs: [] },
      'GET /api/v1/auth/me/tokens': { tokens: [] },
    });
    renderListPage({ path: '/tokens', component: PersonalTokensPage, initial: '/o/acme/tokens' });

    const dialog = await openCreate(user);

    expect(await within(dialog).findByText('You belong to no organization yet.')).toBeInTheDocument();
    expect(within(dialog).queryByRole('checkbox')).not.toBeInTheDocument();
  });

  it('creates a token for two organizations with their caps and shows the secret once', async () => {
    const user = userEvent.setup();
    const posts: unknown[] = [];
    setup({ onPost: (req) => (posts.push(req.body), undefined) });
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByLabelText('Name'), 'laptop');
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Acme' }));
    await pickOption(within(dialog), 'Role in Acme', 'Operator');
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Beta' }));
    await user.click(within(dialog).getByRole('button', { name: 'Create token' }));

    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({
      name: 'laptop',
      kind: 'api',
      grants: [
        { org: 'acme', role: 'operator' },
        { org: 'beta', role: 'viewer' },
      ],
    });
    expect(await within(dialog).findByText('pxp_secret123')).toBeInTheDocument();
    expect(within(dialog).getByText(/you will not see it again/i)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Copy' })).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(screen.queryByText('pxp_secret123')).not.toBeInTheDocument();
  });

  it('offers only the roles up to the person’s own role in each organization', async () => {
    const user = userEvent.setup();
    setup();
    const dialog = await openCreate(user);
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Acme' }));
    await user.click(within(dialog).getByLabelText('Role in Acme'));
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('data-value'))).toEqual(['viewer', 'operator']);
    await user.keyboard('{Escape}');
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Gamma' }));
    await user.click(within(dialog).getByLabelText('Role in Gamma'));
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('data-value'))).toEqual(['viewer', 'operator', 'admin', 'owner']);
  });

  it('shows an organization blocked for the person with the notice, and it cannot be chosen', async () => {
    const user = userEvent.setup();
    setup({ tokens: [token('old', { grants: [grant('gamma', 'viewer', 'blocked')] })] });
    await screen.findByText('1–1 of 1 token');
    const dialog = await openCreate(user);
    const box = within(dialog).getByRole('checkbox', { name: 'Use in Gamma' });
    expect(box).toBeDisabled();
    expect(within(grantRow('Gamma')).getByText('Blocked by an administrator')).toBeInTheDocument();
  });

  it('shows an organization whose switch is off as turned off, and it cannot be chosen', async () => {
    const user = userEvent.setup();
    setup({ off: ['delta'] });
    const dialog = await openCreate(user);
    await waitFor(() => expect(within(dialog).getByRole('checkbox', { name: 'Use in Delta' })).toBeDisabled());
    expect(within(grantRow('Delta')).getByText('Personal tokens are turned off in this organization')).toBeInTheDocument();
  });

  it('limits the assistant areas by the highest chosen role', async () => {
    const user = userEvent.setup();
    setup();
    const dialog = await openCreate(user);
    await user.click(within(dialog).getByRole('radio', { name: 'Assistant token' }));
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Acme' }));
    expect(within(dialog).getByRole('checkbox', { name: 'Inventory: write' })).toBeDisabled();
    await pickOption(within(dialog), 'Role in Acme', 'Operator');
    expect(within(dialog).getByRole('checkbox', { name: 'Inventory: write' })).toBeEnabled();
  });

  it('names the organization a create was refused for', async () => {
    const user = userEvent.setup();
    setup({
      onPost: () =>
        new Response(
          JSON.stringify({ title: 'Conflict', status: 409, detail: 'grant_blocked', errors: [{ location: 'body.grants', message: 'beta' }] }),
          { status: 409, headers: { 'Content-Type': 'application/problem+json' } },
        ),
    });
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByLabelText('Name'), 'laptop');
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Beta' }));
    await user.click(within(dialog).getByRole('button', { name: 'Create token' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('An administrator blocked your personal tokens in Beta.');
  });

  it('drops a new grant refused because the organization turned personal tokens off, so the retry goes through', async () => {
    const user = userEvent.setup();
    const posts: Array<{ grants: unknown[] }> = [];
    setup({
      onPost: (req) => {
        posts.push(req.body as { grants: unknown[] });
        if (posts.length > 1) return undefined;
        return new Response(
          JSON.stringify({ title: 'Conflict', status: 409, detail: 'personal_tokens_off', errors: [{ location: 'body.grants', message: 'delta' }] }),
          { status: 409, headers: { 'Content-Type': 'application/problem+json' } },
        );
      },
    });
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByLabelText('Name'), 'laptop');
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Acme' }));
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Delta' }));
    await user.click(within(dialog).getByRole('button', { name: 'Create token' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Personal tokens are turned off in Delta.');
    const delta = within(dialog).getByRole('checkbox', { name: 'Use in Delta' });
    expect(delta).toBeDisabled();
    expect(delta).not.toBeChecked();

    await user.click(within(dialog).getByRole('button', { name: 'Create token' }));
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1].grants).toEqual([{ org: 'acme', role: 'viewer' }]);
    expect(await within(dialog).findByText('pxp_secret123')).toBeInTheDocument();
  });

  it('edits a token: keeps an unchanged switched-off grant and never sends a blocked one', async () => {
    const user = userEvent.setup();
    const patches: unknown[] = [];
    setup({
      tokens: [
        token('laptop', {
          grants: [grant('acme', 'operator'), grant('delta', 'viewer', 'off'), grant('gamma', 'viewer', 'blocked')],
        }),
      ],
      off: ['delta'],
      onPatch: (req) => (patches.push(req.body), undefined),
    });
    await screen.findByText('1–1 of 1 token');
    await chooseRowAction(user, 'laptop', 'Edit');
    const dialog = await screen.findByRole('dialog', { name: 'Edit token laptop' });
    expect(within(dialog).getByRole('checkbox', { name: 'Use in Delta' })).toBeChecked();
    expect(within(dialog).getByRole('checkbox', { name: 'Use in Gamma' })).toBeDisabled();
    await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Beta' }));
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(patches).toHaveLength(1));
    expect(patches[0]).toEqual({
      grants: [
        { org: 'acme', role: 'operator' },
        { org: 'beta', role: 'viewer' },
        { org: 'delta', role: 'viewer' },
      ],
    });
  });

  it('sends no grants when only the name changes', async () => {
    const user = userEvent.setup();
    const patches: unknown[] = [];
    setup({ tokens: [token('laptop', { grants: [grant('acme', 'operator'), grant('gamma', 'viewer', 'blocked')] })], onPatch: (req) => (patches.push(req.body), undefined) });
    await screen.findByText('1–1 of 1 token');
    await chooseRowAction(user, 'laptop', 'Edit');
    const dialog = await screen.findByRole('dialog', { name: 'Edit token laptop' });
    const name = within(dialog).getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'desk');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patches).toEqual([{ name: 'desk' }]));
  });

  it('offers a switched-off grant only the roles up to its cap', async () => {
    const user = userEvent.setup();
    setup({ tokens: [token('laptop', { grants: [grant('delta', 'operator', 'off')] })], off: ['delta'] });
    await screen.findByText('1–1 of 1 token');
    await chooseRowAction(user, 'laptop', 'Edit');
    const dialog = await screen.findByRole('dialog', { name: 'Edit token laptop' });
    await user.click(within(dialog).getByLabelText('Role in Delta'));
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('data-value'))).toEqual(['viewer', 'operator']);
  });

  it('revokes after a confirmation', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ tokens: [token('laptop')], onDelete: () => deleted++ });
    await screen.findByText('1–1 of 1 token');
    await chooseRowAction(user, 'laptop', 'Revoke…');
    expect(await acceptConfirm(user)).toBe('Revoke token laptop? It stops working in every organization.');
    await waitFor(() => expect(deleted).toBe(1));
    expect(await screen.findByText('Token laptop revoked')).toBeInTheDocument();
  });

  it('lists each organization with its role and marks blocked and switched-off grants', async () => {
    setup({ tokens: [token('laptop', { grants: [grant('acme', 'operator'), grant('gamma', 'viewer', 'blocked'), grant('delta', 'viewer', 'off')] })] });
    const row = (await screen.findByText('laptop')).closest('[data-list-row]') as HTMLElement;
    expect(within(row).getByText('acme: Operator')).toBeInTheDocument();
    expect(within(row).getByText(/gamma: Viewer/)).toHaveTextContent('Blocked by an administrator');
    expect(within(row).getByText(/delta: Viewer/)).toHaveTextContent('Turned off');
  });

  it('sorts by name in the browser and pages at the saved size', async () => {
    const user = userEvent.setup();
    setup({ tokens: numbered('tok', 30).map((n) => token(n)) });
    await screen.findByText('1–25 of 30 tokens');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(0, 3)).toEqual(['tok1', 'tok2', 'tok3']));
  });

  it('speaks German', async () => {
    setup({ locale: 'de', tokens: [token('laptop', { grants: [grant('gamma', 'viewer', 'blocked')] })] });
    expect(await screen.findByText('1–1 von 1 Token')).toBeInTheDocument();
    expect(screen.getByText(/Von einem Admin gesperrt/)).toBeInTheDocument();
  });
});

describe('PersonalTokensPage filters', () => {
  const tokens = [
    token('ci'),
    token('bot', { kind: 'assistant', grants: [grant('beta', 'admin')] }),
    token('stale', { expires_at: '2020-01-01T00:00:00Z' }),
  ];

  it('reads the filters from the URL and narrows the rows', async () => {
    setup({ tokens, initial: '/o/acme/tokens?kind=assistant' });
    expect(await screen.findByText('1–1 of 1 token')).toBeInTheDocument();
    expect(firstCells()).toEqual(['bot']);
    expect(screen.getByText('1 of 3 tokens')).toBeInTheDocument();
  });

  it('writes the organization, the expired chip and the search to the URL', async () => {
    const user = userEvent.setup();
    const router = setup({ tokens });
    await screen.findByText('1–3 of 3 tokens');
    await pickOption(screen, 'Filter by organization', 'Beta');
    await waitFor(() => expect(router.state.location.search).toEqual({ org: 'o2' }));
    expect(firstCells()).toHaveLength(1);
    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
    await user.click(screen.getByRole('button', { name: 'Expired' }));
    await waitFor(() => expect(router.state.location.search).toEqual({ expired: true }));
    expect(firstCells()).toHaveLength(1);
    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
    await user.type(screen.getByRole('searchbox', { name: 'Search tokens' }), 'ci');
    await waitFor(() => expect(router.state.location.search).toEqual({ q: 'ci' }));
    expect(firstCells()).toHaveLength(1);
  });

  it('says when no token matches', async () => {
    setup({ tokens, initial: '/o/acme/tokens?q=zzz' });
    expect(await screen.findByText('No tokens match the filters.')).toBeInTheDocument();
  });
});
