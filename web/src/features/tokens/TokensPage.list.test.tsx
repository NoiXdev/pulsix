import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, openRow, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { TokensPage } from './TokensPage.tsx';
import { validateTokensSearch } from './tokensSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const token = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}-${over.role ?? 'viewer'}`, name, role: 'viewer', kind: 'api', created_at: '2026-01-01T00:00:00Z', ...over,
});

function setup(opts: { locale?: 'de'; tokens?: unknown[]; entries?: unknown[]; initial?: string; onRevoke?: () => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme/personal-tokens': { entries: opts.entries ?? [] }, 'GET /api/v1/orgs/acme/tokens': { tokens: opts.tokens ?? numbered('tok', 30).map((n) => token(n)) },
    'DELETE /api/v1/orgs/acme/tokens/id-tok30-viewer': () => {
      opts.onRevoke?.();
      return {};
    },
  });
  return renderListPage({ path: '/settings/tokens', component: TokensPage, initial: opts.initial ?? '/o/acme/settings/tokens', locale: opts.locale, extraSearch: (q) => ({ ...validateTokensSearch(q) }) });
}

describe('TokensPage list', () => {
  it('sorts by name in the browser, tok9 before tok10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 tokens');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(0, 3)).toEqual(['tok1', 'tok2', 'tok3']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts the role by rank, not alphabetically', async () => {
    const user = userEvent.setup();
    setup({ tokens: [token('a', { role: 'owner' }), token('b', { role: 'admin' }), token('c', { role: 'viewer' })] });
    await screen.findByText('1–3 of 3 tokens');
    await sortBy(user, 'Role');
    await waitFor(() => expect(firstCells()).toEqual(['c', 'b', 'a']));
  });

  it('has Edit and Revoke in the row menu, the same confirmation, no inline buttons', async () => {
    const user = userEvent.setup();
    let revoked = 0;
    setup({ onRevoke: () => revoked++ });
    await screen.findByText('1–25 of 30 tokens');
    expect(screen.queryByRole('button', { name: /^revoke/i })).not.toBeInTheDocument();
    await chooseRowAction(user, 'tok30 (viewer)', 'Revoke…');
    expect(await acceptConfirm(user)).toBe('Revoke token tok30?');
    await waitFor(() => expect(revoked).toBe(1));
    expect(await screen.findByText('Token tok30 revoked')).toBeInTheDocument();
  });

  it('tells tokens of the same name apart in the menu button', async () => {
    setup({ tokens: [token('ci', { role: 'viewer' }), token('ci', { role: 'admin' })] });
    await screen.findByText('1–2 of 2 tokens');
    expect(screen.getByRole('button', { name: 'Actions for ci (viewer)' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Actions for ci (admin)' })).toBeInTheDocument();
  });

  it('offers Edit for a live token and disables it for an expired one', async () => {
    const user = userEvent.setup();
    setup({ tokens: [token('live'), token('old', { expires_at: '2020-01-01T00:00:00Z' })] });
    await screen.findByText('1–2 of 2 tokens');
    await openRowMenu(user, 'live (viewer)');
    expect(await screen.findByRole('menuitem', { name: 'Edit' })).not.toHaveAttribute('aria-disabled');
    await user.keyboard('{Escape}');
    await openRowMenu(user, 'old (viewer)');
    const edit = await screen.findByRole('menuitem', { name: 'Edit' });
    expect(edit).toHaveAttribute('aria-disabled', 'true');
    expect(edit).toHaveAccessibleDescription("Expired tokens can't be edited");
    expect(within(edit).getByText("Expired tokens can't be edited")).toBeVisible();
  });

  it('opens the row\'s dialog from the row itself', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 tokens');
    await openRow(user, 'tok30 (viewer)');
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  it('leaves an expired token\'s row unopened, as its Edit is disabled', async () => {
    setup({ tokens: [token('old', { expires_at: '2020-01-01T00:00:00Z' }), token('fresh')] });
    await screen.findByText('1–2 of 2 tokens');
    expect(screen.getByRole('button', { name: 'fresh (viewer)' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'old (viewer)' })).not.toBeInTheDocument();
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 tokens');
    await hideColumn(user, 'Last used');
    expect(screen.queryByRole('columnheader', { name: 'Last used' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 tokens')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 tokens')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 tokens')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Tokens')).toBeInTheDocument();
  });
});

describe('TokensPage filters', () => {
  const entry = (name: string, over: Record<string, unknown> = {}) => ({
    token_id: `pt-${name}`, name, kind: 'api', role: 'operator', owner_id: 'u-bob', owner_name: 'Bob', owner_email: 'bob@acme.io',
    blocked: false, owner_blocked: false, token_revoked: false, created_at: '2026-01-01T00:00:00Z', ...over,
  });
  const tokens = [
    token('ci', { role: 'admin' }),
    token('bot', { kind: 'assistant', areas: ['hosts'] }),
    token('stale', { expires_at: '2020-01-01T00:00:00Z' }),
  ];

  it('reads the filters from the URL and narrows the rows', async () => {
    setup({ tokens, entries: [entry('laptop')], initial: '/o/acme/settings/tokens?kind=personal' });
    expect(await screen.findByText('1–1 of 1 token')).toBeInTheDocument();
    expect(firstCells()).toEqual(['laptopPersonal']);
    expect(screen.getByText('1 of 4 tokens')).toBeInTheDocument();
  });

  it('writes the owner, the expired chip and the search to the URL', async () => {
    const user = userEvent.setup();
    const router = setup({ tokens, entries: [entry('laptop')] });
    await screen.findByText('1–4 of 4 tokens');
    await pickOption(screen, 'Filter by owner', 'Bob');
    await waitFor(() => expect(router.state.location.search).toEqual({ owner: 'u-bob' }));
    expect(firstCells()).toEqual(['laptopPersonal']);
    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
    await user.click(screen.getByRole('button', { name: 'Expired' }));
    await waitFor(() => expect(router.state.location.search).toEqual({ expired: true }));
    expect(firstCells()).toEqual(['stale']);
    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
    await user.type(screen.getByRole('searchbox', { name: 'Search tokens' }), 'ci');
    await waitFor(() => expect(router.state.location.search).toEqual({ q: 'ci' }));
    expect(firstCells()).toEqual(['ci']);
  });

  it('filters by role and says when no token matches', async () => {
    setup({ tokens, initial: '/o/acme/settings/tokens?role=admin' });
    await screen.findByText('1–1 of 1 token');
    expect(firstCells()).toEqual(['ci']);
  });

  it('says when nothing matches', async () => {
    setup({ tokens, initial: '/o/acme/settings/tokens?q=zzz' });
    expect(await screen.findByText('No tokens match the filters.')).toBeInTheDocument();
  });
});
