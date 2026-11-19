import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { TemplatesPage } from './TemplatesPage.tsx';
import { validateTemplatesSearch } from './templatesSearch.ts';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const PLAYBOOK = '11111111-2222-3333-4444-555555555555';
const playbooks = { playbooks: [{ id: PLAYBOOK, source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '' }, { id: 'pb2', source: 'editor', summary: 'Other', created_at: '', updated_at: '' }] };

function setup(initial: string, posted: unknown[] = [], role = 'operator') {
  mockApi({
    'GET /api/v1/auth/me/preferences': { density: 'standard', page_size: 25, lists: {} },
    'GET /api/v1/orgs/acme': { org: { ...ORG.org, role } },
    'GET /api/v1/orgs/acme/job-templates': { templates: [] },
    'GET /api/v1/orgs/acme/playbooks': playbooks,
    'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
    'GET /api/v1/orgs/acme/repos': { repos: [] },
    'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    'GET /api/v1/orgs/acme/hosts': { hosts: [], next_cursor: '' },
    'POST /api/v1/orgs/acme/job-templates': (req: MockRequest) => {
      posted.push(req.body);
      return { template: { id: 't1', name: 'deploy', playbook_id: PLAYBOOK, scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, revision: 1, created_at: '', updated_at: '' } };
    },
  });
  return renderListPage({ path: '/templates', component: TemplatesPage, initial, extraSearch: (s) => ({ ...validateTemplatesSearch(s) }) });
}

describe('TemplatesPage ?create=', () => {
  it('opens the new template with the playbook chosen, and drops the parameter on close', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    const router = setup(`/o/acme/templates?create=${PLAYBOOK}`, posted);
    const dialog = await screen.findByRole('dialog', { name: 'New job template' });
    await waitFor(() => expect(within(dialog).getByLabelText('Playbook')).toHaveValue('Install nginx'));
    await user.type(within(dialog).getByLabelText('Name'), 'deploy');
    await user.click(within(dialog).getByRole('button', { name: 'Create template' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ name: 'deploy', playbook_id: PLAYBOOK });
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'New job template' })).not.toBeInTheDocument());
    expect(router.state.location.search).not.toHaveProperty('create');
  });

  it('ignores the parameter for someone who may not create templates', async () => {
    setup(`/o/acme/templates?create=${PLAYBOOK}`, [], 'viewer');
    expect(await screen.findByText('No job templates yet.', { exact: false })).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'New job template' })).not.toBeInTheDocument();
  });
});
