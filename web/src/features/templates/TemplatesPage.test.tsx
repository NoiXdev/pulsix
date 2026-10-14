import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { TemplatesPage } from './TemplatesPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const playbookRow = { id: 'pb1', source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '' };
const sshKey = { id: 'c1', name: 'host-login', kind: 'ssh_key', username: 'ansible', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };
const groupRow = { id: 'g1', name: 'webserver', description: '', parent_id: null, members: 0, group_fields: 0, host_fields: 0, complete: true, revision: 1 };

const template = (over: Record<string, unknown> = {}) => ({
  id: 't1', name: 'deploy-site', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] },
  limit: '', extra_vars: {}, created_at: '', updated_at: '', ...over,
});

const withSchema = {
  extra_vars_schema: {
    fields: [
      { name: 'region', type: 'string', required: true },
      { name: 'token', type: 'secret', required: true },
    ],
  },
  extra_vars: { region: 'eu', token: { $secret: 'set' } },
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({ getParentRoute: () => orgRoute, path: '/templates', component: TemplatesPage });
  const jobRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, jobRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/templates'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('TemplatesPage', () => {
  it('lists templates with playbook, scope chip and credential', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': {
        templates: [template({ credential_id: 'c1', scope: { group_id: 'g1', host_ids: [] } })],
      },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [groupRow] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
    });
    renderPage();
    expect(await screen.findByText('deploy-site')).toBeInTheDocument();
    expect(screen.getByText('Install nginx')).toBeInTheDocument();
    expect(screen.getByText('webserver')).toBeInTheDocument();
    expect(await screen.findByText('host-login')).toBeInTheDocument();
  });

  it('creates a template with scope and posts the scope halves', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const templates: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': () => ({ templates }),
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [groupRow] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'web-01', zone_id: 'z1', managed: true, monitored: false, last_seen_at: '', facts: {} }], next_cursor: '' },
      'POST /api/v1/orgs/acme/job-templates': (req: MockRequest) => {
        posted = req.body;
        const created = template({ name: 'deploy-site', scope: { group_id: 'g1', host_ids: ['h1'] }, credential_id: 'c1' });
        templates.push(created);
        return { template: created };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add template' }));
    await user.type(screen.getByLabelText('Name'), 'deploy-site');
    await pickOption(screen, 'Playbook', { value: 'pb1' });
    await pickOption(screen, 'Group', { value: 'g1' });
    await user.click(screen.getByLabelText('Include web-01'));
    await pickOption(screen, 'Default SSH credential', { value: 'c1' });
    await user.click(screen.getByRole('button', { name: 'Create template' }));
    expect(await screen.findByText('deploy-site')).toBeInTheDocument();
    expect(posted).toEqual({
      name: 'deploy-site',
      playbook_id: 'pb1',
      scope: { group_id: 'g1', host_ids: ['h1'] },
      extra_vars: {},
      credential_id: 'c1',
    });
  });

  // v0.4g Task 7 addendum: the Default SSH credential picker's create row
  // was added after Task 5 converted this file's own select — Task 5 left
  // it a plain picker since AddCredentialDialog had no onCreated/initialName
  // yet (see that task's report, Deviation 4).
  it('creates a credential from the Default SSH credential picker and selects it', async () => {
    const user = userEvent.setup();
    let templatePosted: unknown;
    let credPosted: unknown;
    const edgeLogin = {
      id: 'c9', name: 'edge-login', kind: 'ssh_password', username: '', expired: false, expires_soon: false,
      used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '',
    };
    let created = false;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [] },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [groupRow] },
      // A function, not a fixed value — see ReposPage.test.tsx's own
      // comment on why a static list would read "c9 (unavailable)".
      'GET /api/v1/orgs/acme/credentials': () => ({ credentials: created ? [edgeLogin] : [] }),
      'POST /api/v1/orgs/acme/credentials': (req: MockRequest) => {
        credPosted = req.body;
        created = true;
        return { credential: edgeLogin };
      },
      'POST /api/v1/orgs/acme/job-templates': (req: MockRequest) => {
        templatePosted = req.body;
        return { template: template({ name: 'deploy-site', credential_id: 'c9' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add template' }));
    await user.type(screen.getByLabelText('Name'), 'deploy-site');
    await pickOption(screen, 'Playbook', { value: 'pb1' });

    await user.click(screen.getByLabelText('Default SSH credential'));
    await user.keyboard('edge-login');
    await user.click(screen.getByRole('option', { name: /New credential/ }));

    const addDialog = await screen.findByRole('dialog', { name: 'Add credential' });
    expect(within(addDialog).getByLabelText('Name')).toHaveValue('edge-login');
    // Default SSH credential only offers ssh_key/ssh_password — the
    // dialog's own default (ssh_key) would also work, but ssh_password
    // skips the one-time key panel, keeping this test to the one thing
    // it's for.
    await pickOption(within(addDialog), 'Kind', { value: 'ssh_password' });
    await user.type(within(addDialog).getByLabelText('Password'), 'hunter2');
    await user.click(within(addDialog).getByRole('button', { name: 'Create credential' }));

    expect(credPosted).toEqual({ name: 'edge-login', kind: 'ssh_password', password: 'hunter2' });
    expect(screen.getByLabelText('Default SSH credential')).toHaveValue('edge-login');

    await user.click(screen.getByRole('button', { name: 'Create template' }));
    expect(templatePosted).toMatchObject({ credential_id: 'c9' });
  });

  it('runs a template and lands on the run view', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template()] },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'POST /api/v1/orgs/acme/job-templates/t1/run': { jobs: [{ id: 'job-9', zone_id: 'z1', kind: 'run', status: 'queued', run_group_id: 'rg1', trigger: 'manual', created_at: '' }] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Run deploy-site' }));
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
  });

  it('shows an unchanged secret display-only and keeps it with a keep marker on the PUT', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template({ ...withSchema })] },
      'GET /api/v1/orgs/acme/job-templates/t1': { template: template({ ...withSchema, credential_id: 'c1' }) },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'PUT /api/v1/orgs/acme/job-templates/t1': (req: MockRequest) => {
        put = req.body;
        return { template: template({ ...withSchema, credential_id: 'c1' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    const tokenField = await within(dialog).findByLabelText(/token/i);
    expect(tokenField).toHaveAttribute('readonly');
    expect(within(dialog).getByDisplayValue('•••••••• stored')).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    expect(put).toEqual({
      name: 'deploy-site',
      playbook_id: 'pb1',
      scope: undefined,
      extra_vars: { region: 'eu', token: { $secret: 'keep', from: 'token' } },
      extra_vars_schema: withSchema.extra_vars_schema,
      credential_id: 'c1',
    });
  });

  it('sends a replaced secret as plaintext and shows the 422 problems at their fields', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template({ ...withSchema })] },
      'GET /api/v1/orgs/acme/job-templates/t1': { template: template({ ...withSchema, credential_id: 'c1' }) },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'PUT /api/v1/orgs/acme/job-templates/t1': (req: MockRequest) => {
        put = req.body;
        return { template: template({ ...withSchema, credential_id: 'c1' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.click(within(dialog).getByRole('button', { name: 'Replace' }));
    const token = await within(dialog).findByLabelText(/token/i);
    expect(token).toBeEnabled();
    await user.type(token, 'hunter2');
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    const body = put as { extra_vars?: Record<string, unknown>; scope?: unknown };
    expect(body.extra_vars).toEqual({ region: 'eu', token: 'hunter2' });
    expect(body.scope).toBeUndefined();
  });

  it('surfaces a 422 problem at the extra var field it names', async () => {    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template({ ...withSchema })] },
      'GET /api/v1/orgs/acme/job-templates/t1': { template: template({ ...withSchema, extra_vars: { region: '' } }) },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'PUT /api/v1/orgs/acme/job-templates/t1': () =>
        new Response(
          JSON.stringify({ status: 422, title: 'validation failed', errors: [{ location: 'body.extra_vars.region', message: 'is required' }] }),
          { status: 422, headers: { 'Content-Type': 'application/json' } },
        ),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    expect(await within(dialog).findByText('is required')).toBeInTheDocument();
  });

  it('builds the extra-vars form from a schema created in the dialog (live state)', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const templates: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': () => ({ templates }),
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'POST /api/v1/orgs/acme/job-templates': (req: MockRequest) => {
        posted = req.body;
        const created = template({ name: 'deploy-site' });
        templates.push(created);
        return { template: created };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add template' }));
    await user.type(screen.getByLabelText('Name'), 'deploy-site');
    await pickOption(screen, 'Playbook', { value: 'pb1' });

    // Build a schema in the dialog itself: expand it, add a field, rename
    // it, mark it required.
    await user.click(screen.getByText('Extra vars schema'));
    await user.click(screen.getByRole('button', { name: 'Add sub-field' }));
    const fieldGroup = screen.getByRole('group', { name: 'Field field_1' });
    const nameInput = within(fieldGroup).getByLabelText('Name');
    await user.clear(nameInput);
    await user.type(nameInput, 'region');
    await user.click(within(fieldGroup).getByLabelText('Required'));

    // The vars form appeared from the LIVE schema — the field is fillable.
    const dialog = screen.getByRole('dialog', { name: 'New job template' });
    const region = await within(dialog).findByLabelText('region');
    await user.type(region, 'eu');
    await user.click(within(dialog).getByRole('button', { name: 'Create template' }));
    const body = posted as { extra_vars?: Record<string, unknown>; extra_vars_schema?: { fields: unknown[] } };
    expect(body.extra_vars).toEqual({ region: 'eu' });
    expect(body.extra_vars_schema).toEqual({ fields: [{ name: 'region', type: 'string', required: true }] });
  });

  it('renders a strict-validation 422 at the field when a built schema is left unfilled', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [] },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'POST /api/v1/orgs/acme/job-templates': () =>
        new Response(
          JSON.stringify({ status: 422, title: 'validation failed', errors: [{ location: 'body.extra_vars.region', message: 'is required' }] }),
          { status: 422, headers: { 'Content-Type': 'application/json' } },
        ),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add template' }));
    await user.type(screen.getByLabelText('Name'), 'deploy-site');
    await pickOption(screen, 'Playbook', { value: 'pb1' });
    await user.click(screen.getByText('Extra vars schema'));
    await user.click(screen.getByRole('button', { name: 'Add sub-field' }));
    const fieldGroup = screen.getByRole('group', { name: 'Field field_1' });
    const nameInput = within(fieldGroup).getByLabelText('Name');
    await user.clear(nameInput);
    await user.type(nameInput, 'region');
    await user.click(within(fieldGroup).getByLabelText('Required'));

    // The required field stays empty: the 422's problem must be VISIBLE at
    // its field, not swallowed by a vars section that never rendered.
    const dialog = screen.getByRole('dialog', { name: 'New job template' });
    await user.click(within(dialog).getByRole('button', { name: 'Create template' }));
    expect(await within(dialog).findByText('is required')).toBeInTheDocument();
  });
});
