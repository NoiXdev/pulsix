import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { AlertRoutesPage } from './AlertRoutesPage.tsx';
import { validateAlertRoutesSearch } from './notificationsSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const channel = { id: 'c1', kind: 'email', name: 'Ops mail', enabled: true, revision: 1, created_at: '', updated_at: '', email: { recipients: ['ops@example.test'] } };
const route = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, enabled: true, scope: 'org', severities: ['crit'], events: ['opened'], channels: ['c1'], revision: 3, created_at: '', updated_at: '', ...over,
});

function setup(opts: { locale?: 'de'; initial?: string; role?: string; routes?: unknown[]; onDelete?: () => void; onPatch?: (req: MockRequest) => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/auth/me': { user: { id: 'u1', email: 'a@acme.io', display_name: 'A' }, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false },
    'GET /api/v1/orgs/acme': org(opts.role ?? 'admin'),
    'GET /api/v1/orgs/acme/alert-routes': { routes: opts.routes ?? numbered('rt', 30).map((n) => route(n)) },
    'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
    'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
    'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
    'GET /api/v1/orgs/acme/oncall-rotations': { rotations: [] },
    'DELETE /api/v1/orgs/acme/alert-routes/id-rt30': () => {
      opts.onDelete?.();
      return {};
    },
    'PATCH /api/v1/orgs/acme/alert-routes/id-rt30': (req: MockRequest) => {
      opts.onPatch?.(req);
      return { route: route('rt30', { enabled: false }) };
    },
  });
  return renderListPage({ path: '/settings/alert-routes', component: AlertRoutesPage, initial: opts.initial ?? '/o/acme/settings/alert-routes', locale: opts.locale, extraSearch: (q) => ({ ...validateAlertRoutesSearch(q) }) });
}

describe('AlertRoutesPage list', () => {
  it('sorts by route in the browser, rt9 before rt10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 routes');
    await sortBy(user, 'Route');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['rt1', 'rt2', 'rt3', 'rt4', 'rt5', 'rt6', 'rt7', 'rt8', 'rt9', 'rt10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('shows no switch in the row, the state is a read-only Enabled column', async () => {
    setup({ routes: [route('rt30'), route('off', { enabled: false })] });
    await screen.findByText('1–2 of 2 routes');
    expect(screen.queryByRole('switch')).not.toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Enabled' })).toBeInTheDocument();
    const rows = Array.from(document.querySelectorAll<HTMLElement>('[data-list-row]'));
    expect(within(rows[0]).getByText('enabled')).toBeInTheDocument();
    expect(within(rows[1]).getByText('disabled')).toBeInTheDocument();
  });

  it('sorts the Enabled column, enabled first', async () => {
    const user = userEvent.setup();
    setup({ routes: [route('a', { enabled: false }), route('b')] });
    await screen.findByText('1–2 of 2 routes');
    await sortBy(user, 'Enabled');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'a']));
  });

  it('Disable in the menu PATCHes enabled: false with the revision and the column then reads disabled', async () => {
    const user = userEvent.setup();
    let patched: { body: unknown; ifMatch: string | null } | undefined;
    let routes = [route('rt30')];
    mockApi({
      'GET /api/v1/auth/me/preferences': PREFS,
      'GET /api/v1/auth/me': { user: { id: 'u1', email: 'a@acme.io', display_name: 'A' }, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false },
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/alert-routes': () => ({ routes }),
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
      'GET /api/v1/orgs/acme/monitoring-groups': { groups: [] },
      'GET /api/v1/orgs/acme/oncall-rotations': { rotations: [] },
      'PATCH /api/v1/orgs/acme/alert-routes/id-rt30': (req: MockRequest) => {
        patched = { body: req.body, ifMatch: req.headers.get('If-Match') };
        routes = [route('rt30', { enabled: false, revision: 4 })];
        return { route: routes[0] };
      },
    });
    renderListPage({ path: '/settings/alert-routes', component: AlertRoutesPage, initial: '/o/acme/settings/alert-routes' });
    await screen.findByText('enabled');
    expect(screen.queryByRole('menuitem', { name: 'Enable' })).not.toBeInTheDocument();
    await chooseRowAction(user, 'rt30', 'Disable');
    await waitFor(() => expect(patched?.ifMatch).toBe('"3"'));
    expect(patched?.body).toMatchObject({ name: 'rt30', enabled: false, scope: 'org', severities: ['crit'], channels: ['c1'] });
    expect(await screen.findByText('disabled')).toBeInTheDocument();
    await openRowMenu(user, 'rt30');
    expect(screen.getByRole('menuitem', { name: 'Enable' })).toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: 'Disable' })).not.toBeInTheDocument();
  });

  it('has Edit, Disable and Delete for a writer, and Delete asks the same confirmation', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 routes');
    await openRowMenu(user, 'rt30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Edit', 'Disable', 'Delete']);
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    expect(await acceptConfirm(user)).toContain('Delete route rt30?');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('gives an operator no row menu and no Disable entry', async () => {
    setup({ role: 'operator' });
    await screen.findByText('1–25 of 30 routes');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
    expect(screen.queryByRole('menuitem', { name: 'Disable' })).not.toBeInTheDocument();
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 routes');
    await hideColumn(user, 'Fires on');
    expect(screen.queryByRole('columnheader', { name: 'Fires on' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 routes')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 routes')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 routes')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Routen')).toBeInTheDocument();
  });
});

describe('AlertRoutesPage filters', () => {
  const routes = [route('on-a'), route('off-b', { enabled: false }), route('on-c')];

  it('reads the state from the URL and narrows the rows', async () => {
    setup({ routes, initial: '/o/acme/settings/alert-routes?state=disabled' });
    expect(await screen.findByText('1–1 of 1 route')).toBeInTheDocument();
    expect(firstCells()).toEqual(['off-b']);
    expect(screen.getByText('1 of 3 routes')).toBeInTheDocument();
  });

  it('still reads the earlier ?status= link', async () => {
    setup({ routes, initial: '/o/acme/settings/alert-routes?status=disabled' });
    expect(await screen.findByText('1 of 3 routes')).toBeInTheDocument();
    expect(firstCells()).toEqual(['off-b']);
  });

  it('writes the status to the URL and Reset clears it', async () => {
    const user = userEvent.setup();
    const router = setup({ routes });
    await screen.findByText('1–3 of 3 routes');
    await pickOption(screen, 'Filter by state', 'enabled');
    await waitFor(() => expect(router.state.location.search).toEqual({ state: 'enabled' }));
    expect(firstCells().sort()).toEqual(['on-a', 'on-c']);
    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });

  it('sorts the Delivers to and Fires on columns', async () => {
    const user = userEvent.setup();
    setup({ routes: [route('b', { severities: ['warn'] }), route('a', { severities: ['crit'] })] });
    await screen.findByText('1–2 of 2 routes');
    await sortBy(user, 'Fires on');
    await waitFor(() => expect(firstCells()).toEqual(['a', 'b']));
    expect(screen.getByRole('button', { name: 'Delivers to' })).toBeInTheDocument();
  });
});
