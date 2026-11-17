import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, hideColumn, openRowMenu } from '../../test/wholeList.ts';
import { TokensPage } from './TokensPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const orgToken = { id: 'o-ci', name: 'ci', role: 'admin', kind: 'api', created_at: '2026-01-01T00:00:00Z' };
const entry = (name: string, over: Record<string, unknown> = {}) => ({
  token_id: `p-${name}`,
  name,
  kind: 'api',
  owner_id: 'u2',
  owner_name: 'Bob',
  owner_email: 'bob@acme.io',
  role: 'operator',
  blocked: false,
  owner_blocked: false,
  token_revoked: false,
  created_at: '2026-01-01T00:00:00Z',
  ...over,
});

function setup(opts: { entries?: unknown[]; locale?: 'de'; onDelete?: (req: MockRequest) => void; onUnblock?: (req: MockRequest) => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme/tokens': { tokens: [orgToken] },
    'GET /api/v1/orgs/acme/personal-tokens': { entries: opts.entries ?? [entry('laptop')] },
    'DELETE /api/v1/orgs/acme/personal-tokens/p-laptop': (req: MockRequest) => {
      opts.onDelete?.(req);
      return {};
    },
    'POST /api/v1/orgs/acme/personal-tokens/p-old/unblock': (req: MockRequest) => {
      opts.onUnblock?.(req);
      return {};
    },
    'POST /api/v1/orgs/acme/personal-tokens/p-other/unblock': (req: MockRequest) => {
      opts.onUnblock?.(req);
      return {};
    },
  });
  return renderListPage({ path: '/settings/tokens', component: TokensPage, initial: '/o/acme/settings/tokens', locale: opts.locale });
}

function rowOf(text: string): HTMLElement {
  return screen.getByText(text).closest('[data-list-row]') as HTMLElement;
}

describe('TokensPage personal entries', () => {
  it('lists personal entries in one list with the organization tokens, tagged Personal with their owner', async () => {
    setup();
    await screen.findByText('1–2 of 2 tokens');
    const row = rowOf('laptop');
    expect(within(row).getByText('Personal')).toBeInTheDocument();
    expect(within(row).getByText('Bob')).toBeInTheDocument();
    expect(within(row).getByText('Operator')).toBeInTheDocument();
    const ci = rowOf('ci');
    expect(within(ci).queryByText('Personal')).not.toBeInTheDocument();
    expect(within(ci).getByText('—')).toBeInTheDocument();
  });

  it('revokes a personal token for this organization after a confirmation', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–2 of 2 tokens');
    await openRowMenu(user, 'laptop (Bob)');
    expect(screen.queryByRole('menuitem', { name: 'Edit' })).not.toBeInTheDocument();
    await user.click(await screen.findByRole('menuitem', { name: 'Revoke for this organization…' }));
    expect(await acceptConfirm(user)).toMatch(/^Revoke Bob’s personal token laptop for this organization\?/);
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('offers Unblock, not Revoke, for a blocked entry', async () => {
    const user = userEvent.setup();
    let unblocked = 0;
    setup({ entries: [entry('old', { blocked: true, owner_blocked: true, token_revoked: true })], onUnblock: () => unblocked++ });
    await screen.findByText('1–2 of 2 tokens');
    expect(within(rowOf('old')).getByText('Blocked')).toBeInTheDocument();
    await openRowMenu(user, 'old (Bob)');
    expect(screen.queryByRole('menuitem', { name: 'Revoke for this organization…' })).not.toBeInTheDocument();
    await user.click(await screen.findByRole('menuitem', { name: 'Unblock…' }));
    expect(await acceptConfirm(user)).toMatch(/^Lift the block on Bob’s personal tokens/);
    await waitFor(() => expect(unblocked).toBe(1));
  });

  it('marks every entry of a blocked person and offers Unblock on each', async () => {
    const user = userEvent.setup();
    let unblocked = 0;
    setup({
      entries: [entry('old', { blocked: true, owner_blocked: true }), entry('other', { owner_blocked: true })],
      onUnblock: () => unblocked++,
    });
    await screen.findByText('1–3 of 3 tokens');
    expect(within(rowOf('other')).getByText('Blocked (person)')).toBeInTheDocument();
    await chooseRowAction(user, 'other (Bob)', 'Unblock…');
    await acceptConfirm(user);
    await waitFor(() => expect(unblocked).toBe(1));
  });

  it('hides the Owner column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–2 of 2 tokens');
    expect(screen.getByRole('columnheader', { name: 'Owner' })).toBeInTheDocument();
    await hideColumn(user, 'Owner');
    await waitFor(() => expect(screen.queryByRole('columnheader', { name: 'Owner' })).not.toBeInTheDocument());
  });

  it('speaks German', async () => {
    setup({ locale: 'de', entries: [entry('laptop'), entry('other', { owner_blocked: true })] });
    await screen.findByText('1–3 von 3 Tokens');
    expect(within(rowOf('laptop')).getByText('Persönlich')).toBeInTheDocument();
    expect(within(rowOf('other')).getByText('Gesperrt (Person)')).toBeInTheDocument();
  });
});
