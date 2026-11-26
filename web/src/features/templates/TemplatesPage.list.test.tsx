import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { findConfirmDialog } from '../../test/confirmDialog.ts';
import { firstCells, hideColumn, numbered, openRowMenu, sortBy } from '../../test/wholeList.ts';
import { TemplatesPage } from './TemplatesPage.tsx';
import { validateTemplatesSearch } from './templatesSearch.ts';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const PREFS = { density: 'standard', page_size: 25, lists: {} };
const template = (name: string, over: Record<string, unknown> = {}) => ({
  id: `id-${name}`, name, playbook_id: 'pb1', scope: { group_id: null, host_ids: [] },
  limit: '', extra_vars: {}, revision: 1, vars_mode: 'off', created_at: '', updated_at: '', ...over,
});

function setup(over: Record<string, unknown> = {}, started: string[] = [], role = 'operator', initial = '/o/acme/templates', templates?: unknown[]) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme': { org: { ...ORG.org, role } },
    'GET /api/v1/orgs/acme/job-templates': { templates: templates ?? numbered('tpl', 30).map((n) => template(n, over)) },
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
    initial,
    extraSearch: (s) => ({ ...validateTemplatesSearch(s) }),
    others: [
      { path: '/runs/$id', component: () => <div>RUN VIEW</div> },
      { path: '/templates/$id', component: () => <div>TEMPLATE PAGE</div> },
    ],
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
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open', 'Run', 'Variables', 'Triggers', 'Schedules', 'Edit', 'Delete']);
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

  it('links the row name to the template\'s page', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 templates');
    const link = screen.getByRole('link', { name: 'tpl30' });
    expect(link).toHaveAttribute('href', '/o/acme/templates/id-tpl30');
    await user.click(link);
    expect(await screen.findByText('TEMPLATE PAGE')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/templates/id-tpl30');
  });

  it('opens the template\'s page from the row menu', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 templates');
    await openRowMenu(user, 'tpl30');
    await user.click(screen.getByRole('menuitem', { name: 'Open' }));
    expect(await screen.findByText('TEMPLATE PAGE')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/o/acme/templates/id-tpl30');
  });

  it('gives a viewer the link to the page too', async () => {
    setup({}, [], 'viewer');
    await screen.findByText('1–25 of 30 templates');
    expect(screen.getByRole('link', { name: 'tpl30' })).toHaveAttribute('href', '/o/acme/templates/id-tpl30');
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

  it('offers a viewer only Open, Variables, Triggers and Schedules', async () => {
    const user = userEvent.setup();
    setup({}, [], 'viewer');
    await screen.findByText('1–25 of 30 templates');
    await openRowMenu(user, 'tpl30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open', 'Variables', 'Triggers', 'Schedules']);
  });

  it('offers no Delete for an installation-managed template', async () => {
    const user = userEvent.setup();
    setup({ shared_template_id: 'shared-1' });
    await screen.findByText('1–25 of 30 templates');
    await openRowMenu(user, 'tpl30');
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Open', 'Run', 'Variables', 'Triggers', 'Schedules', 'Edit']);
  });
  describe('filters', () => {
    const rows = [
      template('web', { playbook_id: 'pb1' }),
      template('db', { playbook_id: 'pb2', scope: { group_id: 'g1', host_ids: [] } }),
      template('cache', { playbook_id: 'pb1', scope: { group_id: null, host_ids: ['h1'] } }),
    ];

    it('reads the filters from the URL and narrows the rows', async () => {
      setup({}, [], 'operator', '/o/acme/templates?scope=group', rows);
      expect(await screen.findByText('1–1 of 1 template')).toBeInTheDocument();
      expect(firstCells()).toEqual(['db']);
      expect(screen.getByText('1 of 3 templates')).toBeInTheDocument();
    });

    it('writes the playbook and the search to the URL', async () => {
      const user = userEvent.setup();
      const router = setup({}, [], 'operator', '/o/acme/templates', rows);
      await screen.findByText('1–3 of 3 templates');
      await pickOption(screen, 'Filter by playbook', 'Install nginx');
      await waitFor(() => expect(router.state.location.search).toEqual({ playbook: 'pb1' }));
      expect(firstCells().sort()).toEqual(['cache', 'web']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await user.type(screen.getByRole('searchbox', { name: 'Search templates' }), 'cac');
      await waitFor(() => expect(router.state.location.search).toEqual({ q: 'cac' }));
      expect(firstCells()).toEqual(['cache']);
    });

    it('says when no template matches', async () => {
      setup({}, [], 'operator', '/o/acme/templates?q=zzz', rows);
      expect(await screen.findByText('No templates match the filters.')).toBeInTheDocument();
    });
  });
});
