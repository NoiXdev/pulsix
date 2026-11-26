import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { pageRoutes, renderTemplatePage, template } from './templatePageTestKit.tsx';

const header = async () => within(await screen.findByTestId('template-header'));
const started = (id: string) => ({ jobs: [{ id, zone_id: 'z1', kind: 'run', status: 'queued', run_group_id: 'rg1', trigger: 'manual', created_at: '' }] });

describe('TemplateHeader actions', () => {
  it('runs the template and opens the run', async () => {
    const user = userEvent.setup();
    let posted: unknown = 'none';
    mockApi(
      pageRoutes({
        over: {
          'POST /api/v1/orgs/acme/job-templates/t1/run': (req: MockRequest) => ((posted = req.body ?? null), started('job-9')),
        },
      }),
    );
    const { router } = renderTemplatePage();
    await user.click((await header()).getByRole('button', { name: 'Run' }));
    expect(await screen.findByText(/RUN PAGE job-9/)).toBeInTheDocument();
    expect(posted).toBeNull();
    expect(router.state.location.pathname).toBe('/o/acme/runs/job-9');
  });

  it('asks to confirm the hosts a run enrols the agent on, and sends them', async () => {
    const user = userEvent.setup();
    let posted: unknown = 'none';
    mockApi(
      pageRoutes({
        tpl: template({ agent_enrollment: true }),
        over: {
          'GET /api/v1/orgs/acme/job-templates/t1/enrollment-preview': { enrols: true, hosts: [{ id: 'h2', name: 'web-2' }] },
          'POST /api/v1/orgs/acme/job-templates/t1/run': (req: MockRequest) => ((posted = req.body ?? null), started('job-9')),
        },
      }),
    );
    renderTemplatePage();
    await user.click((await header()).getByRole('button', { name: 'Run' }));
    const ask = await screen.findByRole('dialog', { name: 'Enrol agents' });
    expect(ask).toHaveTextContent('Will enrol agents on: web-2.');
    expect(posted).toBe('none');
    await user.click(within(ask).getByRole('button', { name: 'Start and enrol' }));
    expect(await screen.findByText(/RUN PAGE job-9/)).toBeInTheDocument();
    expect(posted).toEqual({ enroll_host_ids: ['h2'] });
  });

  it('asks for the follow-ups of a template that has some before it runs', async () => {
    const user = userEvent.setup();
    mockApi(
      pageRoutes({
        tpl: template({ follow_ups: [{ template_id: 't2', template_name: 'notify', position: 0 }] }),
        over: { 'GET /api/v1/orgs/acme/job-templates': { templates: [template({ follow_ups: [{ template_id: 't2', template_name: 'notify', position: 0 }] }), template({ id: 't2', name: 'notify' })] } },
      }),
    );
    renderTemplatePage();
    await user.click((await header()).getByRole('button', { name: 'Run' }));
    expect(await screen.findByRole('dialog', { name: 'Run deploy-site' })).toBeInTheDocument();
  });

  it("opens the playbook's Dry run tab", async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes());
    const { router } = renderTemplatePage();
    await user.click((await header()).getByRole('button', { name: 'Dry run' }));
    await screen.findByText(/PLAYBOOK PAGE pb1/);
    expect(router.state.location.pathname).toBe('/o/acme/playbooks/pb1');
    expect(router.state.location.search).toEqual({ tab: 'dry-run' });
  });

  it('offers no dry run while the playbook is not synced', async () => {
    mockApi(pageRoutes({ tpl: template({ playbook_id: '' }) }));
    renderTemplatePage();
    expect((await header()).getByRole('button', { name: 'Dry run' })).toBeDisabled();
  });

  it('opens the Settings tab editable from Edit', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes());
    const { router } = renderTemplatePage();
    await user.click((await header()).getByRole('button', { name: 'Edit' }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: 'settings' }));
    const form = await screen.findByRole('form', { name: 'Template settings' });
    expect(within(form).getByLabelText('Name')).toBeEnabled();
    expect(within(form).getByRole('button', { name: 'Save template' })).toBeInTheDocument();
  });

  it('deletes the template from the menu and goes back to the list', async () => {
    const user = userEvent.setup();
    let deleted = false;
    mockApi(
      pageRoutes({
        over: {
          'DELETE /api/v1/orgs/acme/job-templates/t1': () => {
            deleted = true;
            return new Response(null, { status: 204 });
          },
        },
      }),
    );
    const { router } = renderTemplatePage();
    await user.click((await header()).getByRole('button', { name: 'More actions for deploy-site' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await acceptConfirm(user);
    expect(await screen.findByText(/TEMPLATES LIST/)).toBeInTheDocument();
    expect(deleted).toBe(true);
    expect(router.state.location.pathname).toBe('/o/acme/templates');
  });

  it('offers no menu for an installation-managed template', async () => {
    mockApi(pageRoutes({ tpl: template({ shared_template_id: 'shared-1' }) }));
    renderTemplatePage();
    const h = await header();
    expect(h.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
    expect(h.queryByRole('button', { name: 'More actions for deploy-site' })).not.toBeInTheDocument();
  });

  it('offers a viewer no action', async () => {
    mockApi(pageRoutes({ role: 'viewer' }));
    renderTemplatePage();
    const h = await header();
    expect(h.getByRole('heading', { level: 1, name: 'deploy-site' })).toBeInTheDocument();
    await h.findByText('0123456789ab');
    for (const name of ['Run', 'Dry run', 'Edit', 'More actions for deploy-site']) expect(h.queryByRole('button', { name })).not.toBeInTheDocument();
  });
});
