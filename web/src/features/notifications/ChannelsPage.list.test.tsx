import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { ChannelsPage } from './ChannelsPage.tsx';
import { validateChannelsSearch } from './notificationsSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const channel = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`, kind: 'email', name, enabled: true, revision: 1, created_at: '', updated_at: '', email: { recipients: [`${name}@example.test`] }, ...over,
});

function setup(opts: { locale?: 'de'; initial?: string; role?: string; channels?: unknown[]; onDelete?: () => void; onTest?: () => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/auth/me': { user: { id: 'u1', email: 'a@acme.io', display_name: 'A' }, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false },
    'GET /api/v1/orgs/acme': org(opts.role ?? 'admin'),
    'GET /api/v1/orgs/acme/notification-channels': { channels: opts.channels ?? numbered('ch', 30).map((n) => channel(n)), mail_available: true },
    'DELETE /api/v1/orgs/acme/notification-channels/id-ch30': () => {
      opts.onDelete?.();
      return {};
    },
    'POST /api/v1/orgs/acme/notification-channels/id-ch30/test': () => {
      opts.onTest?.();
      return { delivery: { status: 'sent' } };
    },
  });
  return renderListPage({ path: '/settings/notifications', component: ChannelsPage, initial: opts.initial ?? '/o/acme/settings/notifications', locale: opts.locale, extraSearch: (q) => ({ ...validateChannelsSearch(q) }) });
}

describe('ChannelsPage list', () => {
  it('sorts by channel in the browser, ch9 before ch10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 channels');
    await sortBy(user, 'Channel');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['ch1', 'ch2', 'ch3', 'ch4', 'ch5', 'ch6', 'ch7', 'ch8', 'ch9', 'ch10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('has Deliveries, Send test, Edit and Delete in the row menu for a writer', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 channels');
    await openRowMenu(user, 'ch30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Deliveries', 'Send test', 'Edit', 'Delete…']);
  });

  it('deletes through the menu with the same confirmation', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 channels');
    await chooseRowAction(user, 'ch30', 'Delete…');
    expect(await acceptConfirm(user)).toContain('Delete channel ch30?');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('gives an operator Deliveries only', async () => {
    const user = userEvent.setup();
    setup({ role: 'operator' });
    await screen.findByText('1–25 of 30 channels');
    await openRowMenu(user, 'ch30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Deliveries']);
  });

  it('sorts the status, enabled before disabled', async () => {
    const user = userEvent.setup();
    setup({ channels: [channel('a', { enabled: false }), channel('b')] });
    await screen.findByText('1–2 of 2 channels');
    await sortBy(user, 'Status');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'a']));
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 channels');
    await hideColumn(user, 'Kind');
    expect(screen.queryByRole('columnheader', { name: 'Kind' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 channels')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 channels')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 channels')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Kanäle')).toBeInTheDocument();
  });
});

describe('ChannelsPage filters', () => {
  const channels = [
    channel('mail-a'),
    channel('hook-b', { kind: 'webhook', email: undefined, webhook: { method: 'POST', url: 'https://hooks.example.test/x' } }),
    channel('mail-off', { enabled: false }),
  ];

  it('reads the filters from the URL and narrows the rows', async () => {
    setup({ channels, initial: '/o/acme/settings/notifications?kind=webhook' });
    expect(await screen.findByText('1–1 of 1 channel')).toBeInTheDocument();
    expect(firstCells()).toEqual(['hook-b']);
    expect(screen.getByText('1 of 3 channels')).toBeInTheDocument();
  });

  it('writes the status to the URL', async () => {
    const user = userEvent.setup();
    const router = setup({ channels });
    await screen.findByText('1–3 of 3 channels');
    await pickOption(screen, 'Filter by state', 'disabled');
    await waitFor(() => expect(router.state.location.search).toEqual({ state: 'disabled' }));
    expect(firstCells()).toEqual(['mail-off']);
    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });

  it('says when no channel matches', async () => {
    setup({ channels: [channel('a')], initial: '/o/acme/settings/notifications?kind=webhook' });
    expect(await screen.findByText('No channels match the filters.')).toBeInTheDocument();
  });
});
