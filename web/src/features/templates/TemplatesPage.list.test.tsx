import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { findConfirmDialog } from '../../test/confirmDialog.ts';
import { firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { TemplatesPage } from './TemplatesPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const PREFS = { density: 'standard', page_size: 25, lists: {} };
const template = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, playbook_id: 'pb1', scope: { group_id: null, host_ids: [] },
  limit: '', extra_vars: {}, revision: 1, vars_mode: 'off', created_at: '', updated_at: '', ...over,
});

function setup(over: Record<string, unknown> = {}, started: string[] = [], role = 'operator') {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { ...ORG.org, role } },
    'GET /api/v1/orgs/acme/job-templates': { templates: numbered('tpl', 30).map((n) => template(n, over)) },
    'GET /api/v1/orgs/acme/playbooks': { playbooks: [{ id: 'pb1', source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '' }] },
    'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
    'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    'POST /api/v1/orgs/acme/job-templates/id-tpl30/run': (req: MockRequest) => {
      started.push(req.path);
      return { jobs: [{ id: 'j1' }] };
    },
  });
  return renderListPage({
    path: '/templates',
    component: TemplatesPage,
    initial: '/o/acme/templates',
    others: [{ path: '/runs/$id', component: () => <div>RUN VIEW</div> }, { path: '/templates/$id/variables', component: () => <div>VARIABLES</div> }],
  });
}

describe('TemplatesPage list', () => {
  it('sorts by name in the browser, tpl9 before tpl10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 templates');
    await sortBy(user, 'Name');
    await waitFor(() => expect(firstCells().slice(7, 10)).toEqual(['tpl8', 'tpl9', 'tpl10']));
    expect(router.state.location.search).toMatchObject({ sort: 'name', order: 'asc' });
  });

  it('has no inline buttons; the row menu runs the template, and a template with follow-ups opens its dialog', async () => {
    const user = userEvent.setup();
    const started: string[] = [];
    setup({}, started);
    await screen.findByText('1–25 of 30 templates');
    expect(screen.queryByRole('button', { name: 'Run' })).not.toBeInTheDocument();
    await openRowMenu(user, 'tpl30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Run', 'Variables', 'Triggers', 'Schedules', 'Edit', 'Delete']);
    await user.click(screen.getByRole('menuitem', { name: 'Run' }));
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
    expect(started).toHaveLength(1);
  });

  it('opens the follow-ups dialog from Run for a template with follow-ups', async () => {
    const user = userEvent.setup();
    setup({ follow_ups: [{ template_id: 'id-tpl1', name: 'tpl1' }] });
    await screen.findByText('1–25 of 30 templates');
    await openRowMenu(user, 'tpl30');
    await user.click(screen.getByRole('menuitem', { name: 'Run' }));
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  it('Delete opens the same confirmation', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 templates');
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    await openRowMenu(user, 'tpl30');
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    expect(await findConfirmDialog()).toHaveTextContent('Delete template tpl30?');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 templates');
    await hideColumn(user, 'Scope');
    expect(screen.queryByRole('columnheader', { name: 'Scope' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    await user.click(await screen.findByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 templates')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 templates')).toBeInTheDocument();
  });

  it('offers a viewer only Variables, Triggers and Schedules', async () => {
    const user = userEvent.setup();
    setup({}, [], 'viewer');
    await screen.findByText('1–25 of 30 templates');
    await openRowMenu(user, 'tpl30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Variables', 'Triggers', 'Schedules']);
  });

  it('offers no Delete for an installation-managed template', async () => {
    const user = userEvent.setup();
    setup({ shared_template_id: 'shared-1' });
    await screen.findByText('1–25 of 30 templates');
    await openRowMenu(user, 'tpl30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Run', 'Variables', 'Triggers', 'Schedules', 'Edit']);
  });
});
