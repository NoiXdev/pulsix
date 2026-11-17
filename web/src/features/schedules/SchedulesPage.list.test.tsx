import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { findConfirmDialog } from '../../test/confirmDialog.ts';
import { chooseRowAction, firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { SchedulesPage } from './SchedulesPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const PREFS = { density: 'standard', page_size: 25, lists: {} };
const template = { id: 't1', name: 'patch-all', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, created_at: '', updated_at: '' };
const schedule = (name: string, paused = false) => ({
  id: `id-${name}`, template_id: 't1', name, mode: 'cron', cron: '0 3 * * *', description: 'daily',
  timezone: 'Europe/Berlin', guards: { overlap: 'skip', catch_up: { enabled: false, window: '1h' } }, paused,
  next_run_at: '2026-09-22T01:00:00Z', revision: 3, created_at: '', updated_at: '', last_run: null,
});

function setup(role = 'operator', paused = false, posts: string[] = []) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { ...ORG.org, role } },
    'POST /api/v1/orgs/acme/schedules/id-plan30/pause': () => {
      posts.push('pause');
      return {};
    },
    'POST /api/v1/orgs/acme/schedules/id-plan30/resume': () => {
      posts.push('resume');
      return {};
    },
    'GET /api/v1/orgs/acme/job-templates': { templates: [template] },
    'GET /api/v1/orgs/acme/schedules': { schedules: numbered('plan', 30).map((n) => schedule(n, paused)) },
  });
  return renderListPage({
    path: '/schedules',
    component: SchedulesPage,
    initial: '/o/acme/schedules',
    extraSearch: (s) => ({ id: typeof s.id === 'string' ? s.id : undefined, template: typeof s.template === 'string' ? s.template : undefined }),
  });
}

describe('SchedulesPage list', () => {
  it('sorts by name in the browser, plan9 before plan10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 schedules');
    await sortBy(user, 'Schedule');
    await waitFor(() => expect(firstCells().slice(7, 10)).toEqual(['plan8', 'plan9', 'plan10']));
    expect(router.state.location.search).toMatchObject({ sort: 'schedule', order: 'asc' });
  });

  it('keeps no inline buttons; the row menu offers Pause, Edit and Delete, Delete asks first', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 schedules');
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    await openRowMenu(user, 'plan30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Pause', 'Edit', 'Delete']);
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    expect(await findConfirmDialog()).toHaveTextContent('Delete schedule plan30?');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 schedules');
    await hideColumn(user, 'Cron');
    expect(screen.queryByRole('columnheader', { name: 'Cron' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    await user.click(await screen.findByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 schedules')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 schedules')).toBeInTheDocument();
  });

  it('gives a viewer no menu entries', async () => {
    setup('viewer');
    await screen.findByText('1–25 of 30 schedules');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toHaveLength(0);
  });

  it('pauses a running schedule through the menu', async () => {
    const user = userEvent.setup();
    const posts: string[] = [];
    setup('operator', false, posts);
    await screen.findByText('1–25 of 30 schedules');
    await chooseRowAction(user, 'plan30', 'Pause');
    await waitFor(() => expect(posts).toEqual(['pause']));
  });

  it('offers Resume for a paused schedule and posts it', async () => {
    const user = userEvent.setup();
    const posts: string[] = [];
    setup('operator', true, posts);
    await screen.findByText('1–25 of 30 schedules');
    await openRowMenu(user, 'plan30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Resume', 'Edit', 'Delete']);
    await user.click(screen.getByRole('menuitem', { name: 'Resume' }));
    await waitFor(() => expect(posts).toEqual(['resume']));
  });
});
