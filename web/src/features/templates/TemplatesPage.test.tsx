import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { TemplatesPage } from './TemplatesPage.tsx';

const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
// credentials.write is admin-only (lib/roles.ts), stricter than the
// templates.write an operator already has — the credential create row's
// own permission gate (v0.4g Task 7 fix round) needs this role, not ORG's.
const ADMIN_ORG = { org: { ...ORG.org, role: 'admin' } };
const playbookRow = { id: 'pb1', source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '' };
const sshKey = { id: 'c1', name: 'host-login', kind: 'ssh_key', username: 'ansible', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };
const vaultProd = { id: 'c3', name: 'vault-prod', kind: 'vault_password', username: '', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };
const vaultStaging = { id: 'c4', name: 'vault-staging', kind: 'vault_password', username: '', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };
const groupRow = { id: 'g1', name: 'webserver', description: '', parent_id: null, members: 0, group_fields: 0, host_fields: 0, complete: true, revision: 1 };

const template = (over: Record<string, unknown> = {}) => ({
  id: 't1', name: 'deploy-site', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] },
  limit: '', extra_vars: {}, revision: 1, vars_mode: 'off', created_at: '', updated_at: '', ...over,
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
  const variablesRoute = createRoute({ getParentRoute: () => orgRoute, path: '/templates/$id/variables', component: () => <div>VARIABLES</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, jobRoute, variablesRoute])]),
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
      // Admin: credentials.write is required for the create row to appear
      // at all (v0.4g Task 7 fix round) — an operator would not see it.
      'GET /api/v1/orgs/acme': ADMIN_ORG,
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

  // Backlog 2.56: useDeleteTemplate (useTemplates.ts) existed with nothing
  // calling it — the API already cascades a template delete to its
  // schedules and webhook triggers, but the dialog had no delete action at
  // all. Mirrors RepoDialog.tsx's own confirm-then-delete pattern.
  it('deletes a template after confirmation', async () => {
    const user = userEvent.setup();
    let deleted = false;
    window.confirm = () => true;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template()] },
      'GET /api/v1/orgs/acme/job-templates/t1': { template: template() },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'DELETE /api/v1/orgs/acme/job-templates/t1': () => {
        deleted = true;
        return new Response(null, { status: 200 });
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await screen.findByText('Template deploy-site deleted');
    expect(deleted).toBe(true);
    expect(screen.queryByRole('dialog', { name: 'deploy-site' })).not.toBeInTheDocument();
  });

  it('surfaces a toast instead of an unhandled rejection when delete fails', async () => {
    const user = userEvent.setup();
    window.confirm = () => true;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template()] },
      'GET /api/v1/orgs/acme/job-templates/t1': { template: template() },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'DELETE /api/v1/orgs/acme/job-templates/t1': () =>
        new Response(JSON.stringify({ detail: 'template still referenced' }), { status: 409 }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent('template still referenced');
    // The dialog stays open — a failed delete must not act as if it succeeded.
    expect(screen.getByRole('dialog', { name: 'deploy-site' })).toBeInTheDocument();
  });

  it('declines to delete without confirmation', async () => {
    const user = userEvent.setup();
    let deleted = false;
    window.confirm = () => false;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [template()] },
      'GET /api/v1/orgs/acme/job-templates/t1': { template: template() },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'DELETE /api/v1/orgs/acme/job-templates/t1': () => {
        deleted = true;
        return new Response(null, { status: 200 });
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    expect(deleted).toBe(false);
    expect(screen.getByRole('dialog', { name: 'deploy-site' })).toBeInTheDocument();
  });

  it('offers no delete action when creating a new template', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [] },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add template' }));
    const dialog = await screen.findByRole('dialog', { name: 'New job template' });
    expect(within(dialog).queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
  });

  // The extra vars moved to the variables page (backlog 7.75); the dialog's
  // PUT still replaces them, so it echoes them as loaded under If-Match.
  it('saves the settings with If-Match, echoing the loaded schema and the secret as a keep marker', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    const schema = { fields: [...withSchema.extra_vars_schema.fields, { name: 'debug', type: 'bool', active: false }] };
    const loaded = template({ extra_vars_schema: schema, extra_vars: { ...withSchema.extra_vars, debug: true }, credential_id: 'c1', revision: 7 });
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [loaded] },
      'GET /api/v1/orgs/acme/job-templates/t1': { template: loaded },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'PUT /api/v1/orgs/acme/job-templates/t1': (req: MockRequest) => {
        put = req;
        return { template: loaded };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    expect(within(dialog).queryByRole('table', { name: 'Fields' })).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template deploy-site saved');
    expect(put?.headers.get('If-Match')).toBe('"7"');
    expect(put?.body).toEqual({
      name: 'deploy-site',
      playbook_id: 'pb1',
      extra_vars: { region: 'eu', token: { $secret: 'keep', from: 'token' } },
      extra_vars_schema: schema,
      credential_id: 'c1',
    });
  });

  it('re-bases on the current template after a 412, keeping the edits to its settings', async () => {
    const user = userEvent.setup();
    const puts: MockRequest[] = [];
    let current = template({ revision: 1 });
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': () => ({ templates: [current] }),
      'GET /api/v1/orgs/acme/job-templates/t1': () => ({ template: current }),
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      'PUT /api/v1/orgs/acme/job-templates/t1': (req: MockRequest) => {
        puts.push(req);
        if (puts.length === 1) {
          // Meanwhile a sync applied the playbook's variables.
          current = template({ revision: 2, ...withSchema });
          return new Response(JSON.stringify({ detail: 'stale' }), { status: 412 });
        }
        return { template: current };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.type(within(dialog).getByLabelText('Limit pattern'), 'web*');
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    expect(await within(dialog).findByText(/Someone else saved this template since it was opened/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Reload' }));
    await waitFor(() => expect(within(dialog).queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument());
    expect(within(dialog).getByLabelText('Limit pattern')).toHaveValue('web*');
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template deploy-site saved');
    expect(puts[1].headers.get('If-Match')).toBe('"2"');
    expect(puts[1].body).toMatchObject({
      limit: 'web*',
      extra_vars: { region: 'eu', token: { $secret: 'keep', from: 'token' } },
      extra_vars_schema: withSchema.extra_vars_schema,
    });
  });

  it("links a template's variables page from its row and its dialog, with the review badges", async () => {
    const user = userEvent.setup();
    const linked = template({ vars_mode: 'automatic', variables_review: { gone: 2, needs_review: 1, missing_required: 0, left_out: 0 } });
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/job-templates': { templates: [linked] },
      'GET /api/v1/orgs/acme/job-templates/t1': { template: linked },
      'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
    });
    renderPage();
    expect(await screen.findByText('2 fields gone from the source — review')).toBeInTheDocument();
    expect(screen.getByText('1 change needs review')).toBeInTheDocument();
    expect(screen.queryByText(/required/)).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Variables of deploy-site' })).toHaveAttribute('href', '/o/acme/templates/t1/variables');
    await user.click(screen.getByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    expect(within(dialog).getByRole('link', { name: 'Variables' })).toHaveAttribute('href', '/o/acme/templates/t1/variables');
  });

  // v0.2e Task 5: a template's own labelled vault password set, added on
  // top of its repository's (decision 2: a shared label resolves to the
  // template's).
  describe('vault identities', () => {
    it('adds vault identity rows, each with a label and a credential picker, and posts them', async () => {
      const user = userEvent.setup();
      let posted: unknown;
      const templates: Record<string, unknown>[] = [];
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/job-templates': () => ({ templates }),
        'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
        'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey, vaultProd, vaultStaging] },
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

      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 1 label'), 'production');
      await pickOption(screen, 'Vault identity 1 credential', { value: 'c3' });

      await user.click(screen.getByRole('button', { name: 'Create template' }));
      expect(await screen.findByText('deploy-site')).toBeInTheDocument();
      expect(posted).toMatchObject({ vault_identities: [{ label: 'production', credential_id: 'c3' }] });
    });

    it('removes a vault identity row, keeping the rest', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/job-templates': { templates: [] },
        'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
        'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey, vaultProd, vaultStaging] },
      });
      renderPage();
      await user.click(await screen.findByRole('button', { name: 'Add template' }));
      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 1 label'), 'production');
      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 2 label'), 'staging');

      await user.click(screen.getByRole('button', { name: 'Remove vault identity 1' }));
      expect(screen.queryByLabelText('Vault identity 2 label')).not.toBeInTheDocument();
      expect(screen.getByLabelText('Vault identity 1 label')).toHaveValue('staging');
    });

    it('refuses a duplicate vault identity label before submitting', async () => {
      const user = userEvent.setup();
      let posted: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/job-templates': { templates: [] },
        'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
        'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey, vaultProd, vaultStaging] },
        'POST /api/v1/orgs/acme/job-templates': (req: MockRequest) => {
          posted = req.body;
          return { template: template() };
        },
      });
      renderPage();
      await user.click(await screen.findByRole('button', { name: 'Add template' }));
      await user.type(screen.getByLabelText('Name'), 'deploy-site');
      await pickOption(screen, 'Playbook', { value: 'pb1' });

      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 1 label'), 'production');
      await pickOption(screen, 'Vault identity 1 credential', { value: 'c3' });
      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 2 label'), 'production');
      await pickOption(screen, 'Vault identity 2 credential', { value: 'c4' });

      await user.click(screen.getByRole('button', { name: 'Create template' }));
      expect(await screen.findAllByText('duplicate vault identity label "production"')).toHaveLength(2);
      expect(posted).toBeUndefined();
    });

    // The bug class the brief calls out by name: a hand-built save body
    // that forgets a field silently wipes it on an unrelated edit.
    it('keeps an existing template\'s vault identities when an unrelated field is edited', async () => {
      const user = userEvent.setup();
      let put: unknown;
      const withVault = { vault_identities: [{ credential_id: 'c3', label: 'production' }] };
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/job-templates': { templates: [template({ ...withVault })] },
        'GET /api/v1/orgs/acme/job-templates/t1': { template: template({ ...withVault }) },
        'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
        'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey, vaultProd] },
        'PUT /api/v1/orgs/acme/job-templates/t1': (req: MockRequest) => {
          put = req.body;
          return { template: template({ ...withVault }) };
        },
      });
      renderPage();
      await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
      const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
      await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
      expect(put).toMatchObject({ vault_identities: [{ label: 'production', credential_id: 'c3' }] });
    });
  });

  // v0.2f Task 6: a job template that is a projection of an installation-
  // owned definition (spec decisions 1, 2, 6) — the second application of
  // Task 5's own ReposPage test. Name, playbook, limit and extra vars stay
  // visible but read-only; scope and the default SSH credential do not:
  // neither belongs to the definition (decision 2's own table), and no
  // delete action is offered inside an organization today regardless of
  // projection status (useDeleteTemplate has no caller in this GUI yet).
  describe('installation-managed projection (v0.2f)', () => {
    it('marks a projected template in the list and the dialog, and disables its shared fields', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/job-templates': {
          templates: [template({ shared_template_id: 'def-1', playbook_id: '', limit: 'web*' })],
        },
        'GET /api/v1/orgs/acme/job-templates/t1': {
          template: template({ shared_template_id: 'def-1', playbook_id: '', limit: 'web*' }),
        },
        'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
        'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      });
      renderPage();

      expect(await screen.findByText('Installation-managed')).toBeInTheDocument();
      // playbook_id "" (unresolved — this organization has not synced a
      // matching playbook yet) renders as its own label, not an empty cell.
      expect(screen.getByText('not synced yet')).toBeInTheDocument();

      await user.click(screen.getByRole('button', { name: 'deploy-site' }));
      const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });

      // The marker repeats inside the dialog — its own element, not only
      // the list row's.
      expect(within(dialog).getByText('Installation-managed')).toBeInTheDocument();

      // Disabled, not hidden: the value stays on screen even though this
      // organization cannot change it.
      expect(within(dialog).getByLabelText('Name')).toBeDisabled();
      expect(within(dialog).getByLabelText('Name')).toHaveValue('deploy-site');
      expect(within(dialog).getByLabelText('Playbook')).toBeDisabled();
      expect(within(dialog).getByLabelText('Limit pattern')).toBeDisabled();
      expect(within(dialog).getByLabelText('Limit pattern')).toHaveValue('web*');

      // The organization's own scope stays editable; no delete action is
      // offered for a projection — templates.Service.Delete refuses it
      // outright (ErrForbidden), the same reasoning RepoDialog's own
      // `projected` flag already applies to repositories (backlog 2.56
      // added the equivalent button for an ordinary template).
      expect(within(dialog).queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    });

    // Review finding: Save used to stay disabled whenever playbookId was
    // empty, with no exception for a projection — but empty IS a
    // projection's normal state right after its definition is created,
    // before this organization has synced the matching playbook (spec
    // decision 4). That blocked the one thing this dialog still lets the
    // organization edit here at all: its own scope and default SSH
    // credential (decision 2). Asserting the button is merely enabled
    // would not have caught the original bug — the bug was that clicking
    // it did nothing, because it could never be clicked — so this attempts
    // the save and asserts the exact PUT body that reaches the server,
    // including playbook_id staying "" (the server's own Update already
    // accepts that unchanged on a projection; internal/api/
    // shared_template_handlers_test.go's own end-to-end test proves the
    // 200).
    it('saves a projection with an unresolved playbook, sending its own scope and credential', async () => {
      const user = userEvent.setup();
      let put: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/job-templates': {
          templates: [template({ shared_template_id: 'def-1', playbook_id: '', limit: 'web*' })],
        },
        'GET /api/v1/orgs/acme/job-templates/t1': {
          template: template({ shared_template_id: 'def-1', playbook_id: '', limit: 'web*' }),
        },
        'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
        'GET /api/v1/orgs/acme/inventory/groups': { groups: [groupRow] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
        'PUT /api/v1/orgs/acme/job-templates/t1': (req: MockRequest) => {
          put = req.body;
          return { template: template({ shared_template_id: 'def-1', playbook_id: '', limit: 'web*', credential_id: 'c1' }) };
        },
      });
      renderPage();

      await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
      const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });

      const saveButton = within(dialog).getByRole('button', { name: 'Save template' });
      expect(saveButton).not.toBeDisabled();

      // The projection's own field, still editable with an unresolved
      // playbook.
      await pickOption(screen, 'Default SSH credential', { value: 'c1' });
      await user.click(saveButton);

      await screen.findByText('Template deploy-site saved');
      expect(put).toMatchObject({ name: 'deploy-site', playbook_id: '', limit: 'web*', credential_id: 'c1' });
    });

    it('keeps every shared field editable for an ordinary, non-projected template', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/job-templates': { templates: [template()] },
        'GET /api/v1/orgs/acme/job-templates/t1': { template: template() },
        'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
        'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [sshKey] },
      });
      renderPage();

      expect(screen.queryByText('Installation-managed')).not.toBeInTheDocument();

      await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
      const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
      expect(within(dialog).getByLabelText('Name')).not.toBeDisabled();
      expect(within(dialog).getByLabelText('Playbook')).not.toBeDisabled();
      expect(within(dialog).getByLabelText('Limit pattern')).not.toBeDisabled();
    });
  });
});
