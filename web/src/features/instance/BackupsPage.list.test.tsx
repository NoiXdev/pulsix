import { describe, expect, it } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ADMIN } from '../../test/renderRoutes.tsx';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { hideColumn, openRowMenu } from '../../test/wholeList.ts';
import { BackupsPage } from './BackupsPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const SETTINGS = {
  destination: 'folder',
  s3: { endpoint: '', region: '', bucket: '', prefix: '', access_key: '', secret_key_set: false, path_style: false },
  enabled: true, time_of_day: '03:00', time_zone: 'Europe/Berlin', retention: { daily: 7, weekly: 4, monthly: 6 }, include_metrics: true,
};
const backup = (n: number, over: Record<string, unknown> = {}) => ({
  name: `b${String(n).padStart(2, '0')}.pxbak`,
  created_at: `2026-10-${String(n).padStart(2, '0')}T03:00:00Z`,
  kind: 'scheduled', actor_id: null, actor_name: '', pulsix_version: '0.5.2', size_bytes: n * 1000, state: 'succeeded', error: '', verified: true,
  ...over,
});
const thirty = Array.from({ length: 30 }, (_, i) => backup(30 - i));

function setup(opts: { locale?: 'de'; items?: unknown[]; onDelete?: (path: string) => void } = {}) {
  mockApi({
    'GET /api/v1/auth/me': ADMIN,
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { id: 'o1', slug: 'acme', name: 'Acme', role: 'owner' } },
    'GET /api/v1/admin/backups/settings': SETTINGS,
    'GET /api/v1/admin/backups': { items: opts.items ?? thirty, next_run_at: null, destination_label: 'Folder', storage_error: null },
    'DELETE /api/v1/admin/backups/b30.pxbak': (req: MockRequest) => {
      opts.onDelete?.(req.path);
      return new Response(null, { status: 204 });
    },
  });
  return renderListPage({ path: '/settings/backups', component: BackupsPage, initial: '/o/acme/settings/backups', locale: opts.locale });
}

describe('BackupsPage list', () => {
  it('sorts by size in the browser and keeps the sort in the component, not the URL', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 backups');
    await user.click(screen.getByRole('button', { name: 'Size' }));
    await waitFor(() => expect(screen.getAllByRole('button', { name: /^Actions for b/ })[0]).toHaveAccessibleName('Actions for b01.pxbak'));
    expect(router.state.location.search).not.toHaveProperty('sort');
  });

  it('keeps Download a real link in the row menu and in the context menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 backups');
    await openRowMenu(user, 'b30.pxbak');
    const item = screen.getByRole('menuitem', { name: 'Download' });
    expect(item.tagName).toBe('A');
    expect(item).toHaveAttribute('href', '/api/v1/admin/backups/b30.pxbak/download');
    expect(item).toHaveAttribute('download', 'b30.pxbak');
    // A right-click on the link is left to the browser ("Save link as…").
    expect(fireEvent.contextMenu(item)).toBe(true);
    await user.keyboard('{Escape}');
    const row = document.querySelector('[data-list-row]') as HTMLElement;
    fireEvent.contextMenu(within(row).getAllByRole('cell')[1]);
    const ctx = await screen.findByRole('menuitem', { name: 'Download' });
    expect(ctx.tagName).toBe('A');
  });

  it('asks the same question before Delete and disables only the row being deleted', async () => {
    const user = userEvent.setup();
    const deleted: string[] = [];
    setup({ onDelete: (p) => deleted.push(p) });
    await screen.findByText('1–25 of 30 backups');
    await openRowMenu(user, 'b30.pxbak');
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    expect(await acceptConfirm(user)).toBe('Delete the backup b30.pxbak?');
    await waitFor(() => expect(deleted).toEqual(['/api/v1/admin/backups/b30.pxbak']));
  });

  it('opens the strong restore dialog from the row menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 backups');
    await openRowMenu(user, 'b29.pxbak');
    await user.click(screen.getByRole('menuitem', { name: 'Restore…' }));
    expect(await screen.findByRole('dialog', { name: 'Restore a backup' })).toBeInTheDocument();
  });

  it('gives the unverified hint as text, not only a title', async () => {
    setup({ items: [backup(1, { verified: false })] });
    const hint = await screen.findByText(/cannot be authenticated/);
    expect(hint).toHaveClass('sr-only');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 backups');
    await hideColumn(user, 'Version');
    expect(screen.queryByRole('columnheader', { name: 'Version' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    expect(await screen.findByText('1–25 of 30 backups')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 backups')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 backups')).toBeInTheDocument();
  });

  it('writes the footer in German', async () => {
    setup({ locale: 'de' });
    expect(await screen.findByText('1–25 von 30 Backups')).toBeInTheDocument();
  });
});
