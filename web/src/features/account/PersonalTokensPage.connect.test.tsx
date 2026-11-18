import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { chooseRowAction, openRowMenu } from '../../test/wholeList.ts';
import { PersonalTokensPage } from './PersonalTokensPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const ORGS = {
  orgs: [
    { id: 'o1', slug: 'acme', name: 'Acme', role: 'operator', created_at: '2026-01-01T00:00:00Z' },
    { id: 'o2', slug: 'beta', name: 'Beta', role: 'admin', created_at: '2026-01-01T00:00:00Z' },
  ],
};
const grant = (slug: string, state = 'active') => ({ org_id: slug, org_slug: slug, org_name: slug, role: 'viewer', state });
const token = (name: string, kind: string, grants = [grant('acme'), grant('beta'), grant('gone', 'blocked')]) => ({
  id: `id-${name}`,
  name,
  kind,
  areas: kind === 'assistant' ? ['inventory.read'] : undefined,
  created_at: '2026-01-01T00:00:00Z',
  grants,
});
const SH = '~/.local/bin/assistant-mcp.sh';
const O = window.location.origin;

function setup() {
  const routes: Record<string, unknown> = {
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs': ORGS,
    'GET /api/v1/auth/me/tokens': { tokens: [token('bot', 'assistant'), token('script', 'api')] },
    'POST /api/v1/auth/me/tokens': (req: MockRequest) => ({ ...token('new', (req.body as { kind: string }).kind), token: 'pxp_new' }),
  };
  for (const slug of ['acme', 'beta']) routes[`GET /api/v1/orgs/${slug}/settings/personal-tokens`] = { allowed: true };
  mockApi(routes);
  return renderListPage({ path: '/tokens', component: PersonalTokensPage, initial: '/o/acme/tokens' });
}

async function create(user: ReturnType<typeof userEvent.setup>, assistant: boolean) {
  await user.click(await screen.findByRole('button', { name: 'Create token' }));
  const dialog = await screen.findByRole('dialog', { name: 'Create token' });
  await user.type(within(dialog).getByLabelText('Name'), 'fresh');
  await waitFor(() => expect(within(dialog).getByRole('checkbox', { name: 'Use in Acme' })).toBeEnabled());
  await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Acme' }));
  await user.click(within(dialog).getByRole('checkbox', { name: 'Use in Beta' }));
  if (assistant) {
    await user.click(within(dialog).getByRole('radio', { name: 'Assistant token' }));
    await user.click(within(dialog).getByRole('checkbox', { name: 'Inventory: read' }));
  }
  await user.click(within(dialog).getByRole('button', { name: 'Create token' }));
  await within(dialog).findByText('pxp_new');
  return dialog;
}

describe('PersonalTokensPage connect guide', () => {
  it('shows the guide with one server per granted organization under a new assistant token', async () => {
    const user = userEvent.setup();
    setup();
    const dialog = await create(user, true);
    expect(within(dialog).getByText(`${SH} add pulsix-acme ${O} --org acme`)).toBeInTheDocument();
    expect(within(dialog).getByText(`${SH} add pulsix-beta ${O} --org beta`)).toBeInTheDocument();
    expect(within(dialog).getByText(`${SH} check pulsix-beta`)).toBeInTheDocument();
  });

  it('shows no guide under a new API token', async () => {
    const user = userEvent.setup();
    setup();
    const dialog = await create(user, false);
    expect(within(dialog).queryByText(/Connect an assistant/)).not.toBeInTheDocument();
  });

  it('offers the guide from the menu of an assistant token only, for its active organizations', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('script');
    await openRowMenu(user, 'script');
    expect(screen.queryByRole('menuitem', { name: 'Connect an assistant…' })).not.toBeInTheDocument();
    await user.keyboard('{Escape}');
    await chooseRowAction(user, 'bot', 'Connect an assistant…');
    const dialog = await screen.findByRole('dialog', { name: 'Connect an assistant: bot' });
    expect(within(dialog).getByText(`${SH} add pulsix-acme ${O} --org acme`)).toBeInTheDocument();
    expect(within(dialog).queryByText(/--org gone/)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/Copy button above/)).not.toBeInTheDocument();
  });
});
