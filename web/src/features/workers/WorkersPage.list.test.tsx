import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { findConfirmDialog } from '../../test/confirmDialog.ts';
import { firstCells, numbered, openRowMenu } from '../../test/wholeList.ts';
import { WorkersPage } from './WorkersPage.tsx';
import { validateWorkersSearch } from './workersSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const inDays = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString();
const worker = (name: string, extra: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, zone_id: 'z1', zone_slug: 'hel', status: 'online', last_seen: new Date().toISOString(),
  version: 'v0.2c', cert_expires_at: inDays(41), created_at: '', ...extra,
});
const enrollment = (name: string) => ({ id: `e-${name}`, name, zone_id: 'z1', expires_at: inDays(0.02), created_at: '' });

function setup(opts: { role?: string; locale?: 'de'; workers?: unknown[]; initial?: string; enrollments?: unknown[] } = {}) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: opts.role ?? 'admin', created_at: '' } },
    'GET /api/v1/orgs/acme/workers': { workers: opts.workers ?? numbered('wrk', 30).map((n) => worker(n)) },
    'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: opts.enrollments ?? numbered('enr', 30).map(enrollment) },
  });
  return renderListPage({ path: '/workers', component: WorkersPage, initial: opts.initial ?? '/o/acme/workers', locale: opts.locale, extraSearch: (q) => ({ ...validateWorkersSearch(q) }), others: [{ path: '/runs/$id', component: () => <div>RUN VIEW</div> }] });
}

/** The list's card: the table and its pager (the pager sits beside the table). */
const card = (table: HTMLElement) => within(table.parentElement as HTMLElement);
const workerTable = () => screen.getByRole('table', { name: 'Workers' });
const enrollmentTable = () => screen.getByRole('table', { name: 'Pending enrollments' });

describe('WorkersPage lists', () => {
  it('sorts the workers by name in the browser, wrk9 before wrk10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 workers');
    await user.click(within(workerTable()).getByRole('button', { name: 'Worker' }));
    await waitFor(() => expect(firstCells().slice(0, 10)).toEqual(['wrk1', 'wrk2', 'wrk3', 'wrk4', 'wrk5', 'wrk6', 'wrk7', 'wrk8', 'wrk9', 'wrk10']));
    expect(router.state.location.search).toMatchObject({ sort: 'worker', order: 'asc' });
  });

  it('keeps the enrollment list out of the URL', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 enrollments');
    await user.click(within(enrollmentTable()).getByRole('button', { name: 'Pending enrollment' }));
    await user.click(card(enrollmentTable()).getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 enrollments')).toBeInTheDocument();
    expect(router.state.location.search).toEqual({});
    expect(screen.getByText('1–25 of 30 workers')).toBeInTheDocument();
  });

  it('keeps no inline buttons; the row menu opens the same revoke confirmation', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 workers');
    expect(screen.queryByRole('button', { name: /^Revoke/ })).not.toBeInTheDocument();
    await openRowMenu(user, 'wrk30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Revoke…']);
    await user.click(screen.getByRole('menuitem', { name: 'Revoke…' }));
    expect(await findConfirmDialog()).toHaveTextContent('Revoke worker wrk30?');
  });

  it('offers Delete, not Revoke, for a revoked worker', async () => {
    const user = userEvent.setup();
    setup({ workers: [worker('old', { status: 'revoked', cert_expires_at: null })] });
    await openRowMenu(user, 'old');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Delete…']);
  });

  it('withdraws an enrollment from its own row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 enrollments');
    await user.click(screen.getByRole('button', { name: 'Actions for enrollment enr30' }));
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Withdraw']);
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 workers');
    await user.click(screen.getAllByRole('button', { name: /^Columns/ })[0]);
    await user.click(within(await screen.findByRole('dialog', { name: 'Columns' })).getByRole('checkbox', { name: 'Version' }));
    expect(within(workerTable()).queryByRole('columnheader', { name: 'Version' })).not.toBeInTheDocument();
  });

  it('pages 30 workers and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 workers')).toBeInTheDocument();
    await user.click(card(workerTable()).getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 workers')).toBeInTheDocument();
    await pickOption(card(workerTable()), 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 workers')).toBeInTheDocument();
  });

  it('writes the footers in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Worker')).toBeInTheDocument();
    expect(screen.getByText('1–25 von 30 Registrierungen')).toBeInTheDocument();
  });

  describe('filters', () => {
    const workers = [worker('a'), worker('b', { zone_slug: 'fra' }), worker('c', { status: 'revoked' })];

    it('reads the zone and status from the URL', async () => {
      setup({ workers, enrollments: [], initial: '/o/acme/workers?zone=fra' });
      await screen.findByText('1–1 of 1 worker');
      expect(firstCells()).toEqual(['b']);
      expect(screen.getByText('1 of 3 workers')).toBeInTheDocument();
    });

    it('writes the status to the URL and resets', async () => {
      const user = userEvent.setup();
      const router = setup({ workers, enrollments: [], initial: '/o/acme/workers?page=2' });
      await screen.findByText('1–3 of 3 workers');
      await pickOption(screen, 'Status', 'revoked');
      await waitFor(() => expect(router.state.location.search).toEqual({ status: 'revoked' }));
      expect(firstCells()).toEqual(['c']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await waitFor(() => expect(router.state.location.search).toEqual({}));
      expect(firstCells()).toHaveLength(3);
    });

    it('filters the pending enrollments to the expired ones without touching the URL', async () => {
      const user = userEvent.setup();
      const router = setup({
        workers,
        enrollments: [{ ...enrollment('fresh'), expires_at: inDays(1) }, { ...enrollment('stale'), expires_at: inDays(-1) }],
      });
      await screen.findByText('1–2 of 2 enrollments');
      await user.click(screen.getByRole('button', { name: 'Expired' }));
      expect(await screen.findByText('1–1 of 1 enrollment')).toBeInTheDocument();
      expect(router.state.location.search).toEqual({});
    });
  });
});
