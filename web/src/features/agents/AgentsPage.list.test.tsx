import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { findConfirmDialog } from '../../test/confirmDialog.ts';
import { firstCells, numbered, openRowMenu } from '../../test/wholeList.ts';
import { AgentsPage } from './AgentsPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const inDays = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString();
const agent = (name: string) => ({
  host_id: `id-${name}`, host_name: name, agent_name: name, status: 'online', last_seen: new Date().toISOString(),
  version: 'v0.3a', cert_expires_at: inDays(60), created_at: '',
});
const token = (name: string) => ({ id: `t-${name}`, zone_id: 'z1', name_hint: name, max_uses: 3, uses: 1, expires_at: inDays(0.02), created_at: '' });

function setup(opts: { role?: string; locale?: 'de'; initial?: string } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: opts.role ?? 'operator', created_at: '' } },
    'GET /api/v1/orgs/acme/agents': { agents: numbered('agent', 30).map(agent) },
    'GET /api/v1/orgs/acme/agents/tokens': { tokens: numbered('tok', 30).map(token) },
  });
  return renderListPage({
    path: '/agents', component: AgentsPage, initial: opts.initial ?? '/o/acme/agents', locale: opts.locale,
    others: [{ path: '/hosts/$id', component: () => <div>HOST VIEW</div> }],
  });
}

/** The list's card: the table and its pager (the pager sits beside the table). */
const card = (table: HTMLElement) => within(table.parentElement as HTMLElement);
const agentTable = () => screen.getByRole('table', { name: 'Agents' });
const tokenTable = () => screen.getByRole('table', { name: 'Pending enrollment tokens' });

describe('AgentsPage lists', () => {
  it('sorts the agents by host in the browser, agent9 before agent10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 agents');
    await user.click(within(agentTable()).getByRole('button', { name: 'Host' }));
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['agent1', 'agent2', 'agent3', 'agent4', 'agent5', 'agent6', 'agent7', 'agent8', 'agent9', 'agent10']));
    expect(router.state.location.search).toMatchObject({ sort: 'host', order: 'asc' });
  });

  it('keeps the token list out of the URL: its sort and page are component state', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 tokens');
    await user.click(within(tokenTable()).getByRole('button', { name: 'Pending enrollment token' }));
    await user.click(card(tokenTable()).getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 tokens')).toBeInTheDocument();
    expect(router.state.location.search).toEqual({});
    // The agents list did not move.
    expect(screen.getByText('1–25 of 30 agents')).toBeInTheDocument();
  });

  it('sorts the tokens in the browser, tok9 before tok10', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 tokens');
    await user.click(within(tokenTable()).getByRole('button', { name: 'Pending enrollment token' }));
    await waitFor(() => {
      const names = within(tokenTable()).getAllByRole('row').slice(1, 11).map((r) => r.querySelector('[role="cell"]')?.textContent);
      expect(names).toEqual(['tok1', 'tok2', 'tok3', 'tok4', 'tok5', 'tok6', 'tok7', 'tok8', 'tok9', 'tok10']);
    });
  });

  it('keeps no inline buttons; the row menus open the same confirmation', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 agents');
    expect(screen.queryByRole('button', { name: /^Revoke/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Withdraw' })).not.toBeInTheDocument();
    await openRowMenu(user, 'agent30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open host', 'Revoke…']);
    await user.click(screen.getByRole('menuitem', { name: 'Revoke…' }));
    expect(await findConfirmDialog()).toHaveTextContent('Revoke the agent on agent30?');
  });

  it('offers a viewer only Open host on an agent, and nothing on a token', async () => {
    const user = userEvent.setup();
    setup({ role: 'viewer' });
    await screen.findByText('1–25 of 30 agents');
    await openRowMenu(user, 'agent30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open host']);
    expect(screen.queryByRole('button', { name: 'Actions for token tok30' })).not.toBeInTheDocument();
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 agents');
    await user.click(screen.getAllByRole('button', { name: /^Columns/ })[0]);
    await user.click(within(await screen.findByRole('dialog', { name: 'Columns' })).getByRole('checkbox', { name: 'Version' }));
    expect(within(agentTable()).queryByRole('columnheader', { name: 'Version' })).not.toBeInTheDocument();
  });

  it('pages 30 agents and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 agents')).toBeInTheDocument();
    await user.click(card(agentTable()).getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 agents')).toBeInTheDocument();
    await pickOption(card(agentTable()), 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 agents')).toBeInTheDocument();
  });

  it('writes the footers in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Agents')).toBeInTheDocument();
    expect(screen.getByText('1–25 von 30 Token')).toBeInTheDocument();
  });
});
