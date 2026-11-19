import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, openRow, sortBy } from '../../test/wholeList.ts';
import { RotationsPage } from './RotationsPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const rotation = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, period_days: 7, anchor: '2026-01-05T09:00:00+01:00', timezone: 'Europe/Berlin', channels: [], revision: 1, created_at: '', updated_at: '', ...over,
});

function setup(opts: { locale?: 'de'; role?: string; rotations?: unknown[]; onDelete?: () => void } = {}) {
  const list = (opts.rotations ?? numbered('rot', 30).map((n) => rotation(n))) as Array<{ id: string }>;
  const routes: Record<string, unknown> = {
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/auth/me': { user: { id: 'u1', email: 'a@acme.io', display_name: 'A' }, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false },
    'GET /api/v1/orgs/acme': org(opts.role ?? 'admin'),
    'GET /api/v1/orgs/acme/oncall-rotations': { rotations: list },
    'GET /api/v1/orgs/acme/notification-channels': { channels: [], mail_available: true },
    'DELETE /api/v1/orgs/acme/oncall-rotations/id-rot30': () => {
      opts.onDelete?.();
      return {};
    },
  };
  for (const r of list) {
    routes[`GET /api/v1/orgs/acme/oncall-rotations/${r.id}/shifts`] = { shifts: [] };
    routes[`GET /api/v1/orgs/acme/oncall-rotations/${r.id}/exceptions`] = { exceptions: [] };
  }
  mockApi(routes);
  return renderListPage({ path: '/settings/oncall', component: RotationsPage, initial: '/o/acme/settings/oncall', locale: opts.locale });
}

describe('RotationsPage list', () => {
  it('sorts by rotation in the browser, rot9 before rot10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 rotations');
    await sortBy(user, 'Rotation');
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['rot1', 'rot2', 'rot3', 'rot4', 'rot5', 'rot6', 'rot7', 'rot8', 'rot9', 'rot10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('sorts the period as a number', async () => {
    const user = userEvent.setup();
    setup({ rotations: [rotation('a', { period_days: 10 }), rotation('b', { period_days: 2 }), rotation('c', { period_days: 9 })] });
    await screen.findByText('1–3 of 3 rotations');
    await sortBy(user, 'Period');
    await waitFor(() => expect(firstCells()).toEqual(['b', 'c', 'a']));
  });

  it('has Edit and Delete in the row menu for a writer, the same confirmation, no inline buttons', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    setup({ onDelete: () => deleted++ });
    await screen.findByText('1–25 of 30 rotations');
    expect(screen.queryByRole('button', { name: /^Delete rot/ })).not.toBeInTheDocument();
    await chooseRowAction(user, 'rot30', 'Delete');
    expect(await acceptConfirm(user)).toContain('Delete rotation rot30?');
    await waitFor(() => expect(deleted).toBe(1));
  });

  it('gives an operator no row menu', async () => {
    setup({ role: 'operator' });
    await screen.findByText('1–25 of 30 rotations');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toEqual([]);
  });

  it('opens the row\'s dialog from the row itself', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 rotations');
    await openRow(user, 'rot30');
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  it('gives a operator a row that opens nothing', async () => {
    setup({ role: 'operator' });
    await screen.findByText('1–25 of 30 rotations');
    expect(screen.getAllByText('rot30').length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: 'rot30' })).not.toBeInTheDocument();
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 rotations');
    await hideColumn(user, 'Cycle');
    expect(screen.queryByRole('columnheader', { name: 'Cycle' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 rotations')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 rotations')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 rotations')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Rotationen')).toBeInTheDocument();
  });
});
