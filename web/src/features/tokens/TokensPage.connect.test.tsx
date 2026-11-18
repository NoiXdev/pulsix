// @vitest-environment-options {"url": "https://localhost:3000"}
import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { chooseRowAction, openRowMenu } from '../../test/wholeList.ts';
import { TokensPage } from './TokensPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const tok = (id: string, name: string, kind: string) => ({ id, name, role: 'viewer', kind, areas: kind === 'assistant' ? ['inventory.read'] : undefined, created_at: '2026-01-01T00:00:00Z' });
const personal = {
  token_id: 'p-bob',
  name: 'bobs',
  kind: 'assistant',
  owner_id: 'u2',
  owner_name: 'Bob',
  owner_email: 'bob@acme.io',
  role: 'operator',
  blocked: false,
  owner_blocked: false,
  token_revoked: false,
  created_at: '2026-01-01T00:00:00Z',
};
const ADD = `~/.local/bin/assistant-mcp.sh add pulsix-acme ${window.location.origin}`;

function setup(role = 'admin') {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: { body: unknown }) => req.body,
    'GET /api/v1/orgs/acme/tokens': { tokens: [tok('t1', 'ci', 'api'), tok('t2', 'helper', 'assistant'), { ...tok('t4', 'old', 'assistant'), expires_at: '2020-01-01T00:00:00Z' }] },
    'GET /api/v1/orgs/acme/personal-tokens': { entries: [personal] },
    'GET /api/v1/orgs': { orgs: [{ id: 'o1', slug: 'acme', name: 'Acme', role, created_at: '2026-01-01T00:00:00Z' }] },
    'POST /api/v1/orgs/acme/tokens': (req: { body: unknown }) => ({
      ...tok('t3', (req.body as { name: string }).name, (req.body as { kind: string }).kind),
      token: 'pxt_new',
    }),
  });
  return renderListPage({ path: '/settings/tokens', component: TokensPage, initial: '/o/acme/settings/tokens' });
}

async function create(user: ReturnType<typeof userEvent.setup>, assistant: boolean) {
  await user.click(await screen.findByRole('button', { name: 'Create token' }));
  await user.type(screen.getByLabelText('Name'), 'fresh');
  if (assistant) {
    await user.click(screen.getByRole('radio', { name: 'Assistant token' }));
    await user.click(screen.getByRole('checkbox', { name: 'Inventory: read' }));
  }
  const buttons = screen.getAllByRole('button', { name: 'Create token' });
  await user.click(buttons[buttons.length - 1]!);
  await screen.findByText('pxt_new');
}

describe('TokensPage connect guide', () => {
  it('shows the guide under a new assistant token, pointing at the secret above', async () => {
    const user = userEvent.setup();
    setup();
    await create(user, true);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText(ADD)).toBeInTheDocument();
    expect(within(dialog).getByText(/Copy button above/)).toBeInTheDocument();
  });

  it('links Settings → General under the secret for an admin', async () => {
    const user = userEvent.setup();
    setup('admin');
    await create(user, true);
    expect(await within(screen.getByRole('dialog')).findByRole('link', { name: 'Settings → General' })).toHaveAttribute('href', '/o/acme/settings/general');
  });

  it('names Settings → General as plain text for an operator', async () => {
    const user = userEvent.setup();
    setup('operator');
    await create(user, true);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText(/Settings → General/)).toBeInTheDocument();
    expect(within(dialog).queryByRole('link')).not.toBeInTheDocument();
  });

  it('puts Copy by the secret and Done after the guide', async () => {
    const user = userEvent.setup();
    setup();
    await create(user, true);
    const dialog = screen.getByRole('dialog');
    const copy = within(dialog).getByRole('button', { name: 'Copy' });
    const done = within(dialog).getByRole('button', { name: 'Done' });
    const guide = within(dialog).getByRole('region', { name: 'Connect an assistant' });
    expect(copy.compareDocumentPosition(guide) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(guide.compareDocumentPosition(done) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('does not offer the guide on an expired assistant token', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('old');
    await openRowMenu(user, 'old (viewer)');
    expect(screen.queryByRole('menuitem', { name: 'Connect an assistant…' })).not.toBeInTheDocument();
  });

  it('shows no guide under a new API token', async () => {
    const user = userEvent.setup();
    setup();
    await create(user, false);
    expect(within(screen.getByRole('dialog')).queryByText(/Connect an assistant/)).not.toBeInTheDocument();
  });

  it('offers "Connect an assistant…" on the organization’s assistant token only, without the secret hint', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('helper');
    await chooseRowAction(user, 'helper (viewer)', 'Connect an assistant…');
    const dialog = await screen.findByRole('dialog', { name: 'Connect an assistant: helper' });
    expect(within(dialog).getByText(ADD)).toBeInTheDocument();
    expect(within(dialog).queryByText(/Copy button above/)).not.toBeInTheDocument();
    expect(within(dialog).getByText(/saved when it was created/)).toBeInTheDocument();
  });

  it('does not offer it on an API token or on a member’s personal token', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('helper');
    await openRowMenu(user, 'ci (viewer)');
    expect(screen.queryByRole('menuitem', { name: 'Connect an assistant…' })).not.toBeInTheDocument();
    await user.keyboard('{Escape}');
    await openRowMenu(user, 'bobs (Bob)');
    expect(screen.queryByRole('menuitem', { name: 'Connect an assistant…' })).not.toBeInTheDocument();
  });
});
