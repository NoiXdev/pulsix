import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { chooseRowAction, openRowMenu } from '../../test/wholeList.ts';
import { MembersPage } from './MembersPage.tsx';
import { validateMembersSearch } from './membersSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const MEMBER = { user_id: 'u-a', display_name: 'a', email: 'a@acme.io', role: 'admin', created_at: '2026-01-01T00:00:00Z', disabled_at: null };
const invite = (email: string, over: Record<string, unknown> = {}) => ({
  id: `i-${email.split('@')[0]}`, email, role: 'viewer', invited_by: 'u1', invited_by_name: 'Tim',
  expires_at: '2099-01-08T00:00:00Z', created_at: '2026-01-01T00:00:00Z', expired: false, ...over,
});
const resent = (email: string, over: Record<string, unknown> = {}) => ({
  id: `i-${email.split('@')[0]}`, email, role: 'viewer', expires_at: '2099-01-15T00:00:00Z', mail_sent: true, ...over,
});

interface Setup {
  role?: string;
  locale?: 'de';
  invites?: unknown[];
  /** The resend answer for i-old, or a function returning it. */
  resend?: unknown;
  lists?: URLSearchParams[];
  posts?: string[];
}

function setup(opts: Setup = {}) {
  const invites = opts.invites ?? [invite('fresh@acme.io'), invite('old@acme.io', { expires_at: '2026-01-08T00:00:00Z', expired: true })];
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: opts.role ?? 'admin', created_at: '' } },
    'GET /api/v1/orgs/acme/members': { members: [MEMBER] },
    'GET /api/v1/orgs/acme/invites': (req: MockRequest) => {
      opts.lists?.push(req.search);
      return { invites };
    },
    'POST /api/v1/orgs/acme/invites/i-old/resend': (req: MockRequest) => {
      opts.posts?.push(req.path);
      if (typeof opts.resend === 'function') return (opts.resend as () => unknown)();
      return opts.resend ?? resent('old@acme.io');
    },
    'POST /api/v1/orgs/acme/invites/i-fresh/resend': (req: MockRequest) => {
      opts.posts?.push(req.path);
      return resent('fresh@acme.io');
    },
  });
  renderListPage({
    path: '/settings/members', component: MembersPage, initial: '/o/acme/settings/members', locale: opts.locale, extraSearch: (s) => ({ ...validateMembersSearch(s) }),
    others: [{ path: '/settings/members/$userId', component: () => <div>MEMBER PAGE</div> }],
  });
  return screen.findByRole('region', { name: opts.locale === 'de' ? 'Einladungen' : 'Invitations' });
}

const rowOf = (section: HTMLElement, email: string) =>
  within(section).getAllByRole('row').find((r) => within(r).queryByText(email)) as HTMLElement;

describe('PendingInvites resend', () => {
  it('asks for the expired invitations too', async () => {
    const lists: URLSearchParams[] = [];
    await setup({ lists });
    expect(lists[0]?.get('include_expired')).toBe('true');
  });

  it('marks an expired invitation and shows a Resend button on it only', async () => {
    const section = await setup();
    await within(section).findByText('old@acme.io');
    const old = rowOf(section, 'old@acme.io');
    expect(within(old).getByText('Expired')).toBeInTheDocument();
    expect(within(old).getByRole('button', { name: 'Resend the invitation to old@acme.io' })).toHaveTextContent('Resend');
    const fresh = rowOf(section, 'fresh@acme.io');
    expect(within(fresh).queryByText('Expired')).not.toBeInTheDocument();
    expect(within(fresh).queryByRole('button', { name: /^Resend/ })).not.toBeInTheDocument();
  });

  it('resends from the button, toasts and reloads the list', async () => {
    const user = userEvent.setup();
    const lists: URLSearchParams[] = [];
    const posts: string[] = [];
    const section = await setup({ lists, posts });
    await user.click(await within(section).findByRole('button', { name: 'Resend the invitation to old@acme.io' }));
    expect(await screen.findByText('Invitation to old@acme.io sent again')).toBeInTheDocument();
    expect(posts).toEqual(['/api/v1/orgs/acme/invites/i-old/resend']);
    await waitFor(() => expect(lists.length).toBeGreaterThan(1));
  });

  it('offers Resend invitation in every row menu', async () => {
    const user = userEvent.setup();
    const posts: string[] = [];
    await setup({ posts });
    await openRowMenu(user, 'fresh@acme.io (viewer)');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Resend invitation', 'Revoke…']);
    await user.click(screen.getByRole('menuitem', { name: 'Resend invitation' }));
    expect(await screen.findByText('Invitation to fresh@acme.io sent again')).toBeInTheDocument();
    expect(posts).toEqual(['/api/v1/orgs/acme/invites/i-fresh/resend']);
  });

  it('shows the link to hand over when no e-mail went out', async () => {
    const user = userEvent.setup();
    const link = 'http://localhost:3000/invite#token=tok_again';
    await setup({ resend: resent('old@acme.io', { mail_sent: false, accept_url: link }) });
    await chooseRowAction(user, 'old@acme.io (viewer)', 'Resend invitation');
    const dialog = await screen.findByRole('dialog', { name: 'Share the invitation link' });
    expect(within(dialog).getByLabelText('Invitation link')).toHaveTextContent(link);
    expect(screen.queryByText('Invitation to old@acme.io sent again')).not.toBeInTheDocument();
  });

  it('says the invitation is unchanged when the mail could not be sent', async () => {
    const user = userEvent.setup();
    await setup({
      resend: () => new Response(JSON.stringify({ status: 502, detail: 'the invitation e-mail could not be delivered' }), { status: 502, headers: { 'Content-Type': 'application/json' } }),
    });
    await chooseRowAction(user, 'old@acme.io (viewer)', 'Resend invitation');
    expect(await screen.findByText('The invitation e-mail could not be sent. The invitation is unchanged.')).toBeInTheDocument();
  });

  it('claims nothing about the invitation on a bare 502 from a proxy', async () => {
    const user = userEvent.setup();
    await setup({
      resend: () => new Response('<html><body>502 Bad Gateway</body></html>', { status: 502, headers: { 'Content-Type': 'text/html' } }),
    });
    await chooseRowAction(user, 'old@acme.io (viewer)', 'Resend invitation');
    expect(await screen.findByText('failed to resend invitation')).toBeInTheDocument();
    expect(screen.queryByText(/The invitation is unchanged/)).not.toBeInTheDocument();
  });

  it('offers no resend without members.manage', async () => {
    const user = userEvent.setup();
    const section = await setup({ role: 'operator' });
    await within(section).findByText('old@acme.io');
    expect(within(section).queryByRole('button', { name: /^Resend/ })).not.toBeInTheDocument();
    await openRowMenu(user, 'old@acme.io (viewer)');
    expect(screen.queryByRole('menuitem', { name: 'Resend invitation' })).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    const section = await setup({ locale: 'de' });
    await within(section).findByText('old@acme.io');
    const old = rowOf(section, 'old@acme.io');
    expect(within(old).getByText('Abgelaufen')).toBeInTheDocument();
    expect(within(old).getByRole('button', { name: 'Einladung an old@acme.io erneut senden' })).toHaveTextContent('Erneut senden');
  });
});
