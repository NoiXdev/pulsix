import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';
import { pageRoutes, renderTemplatePage, template } from './templatePageTestKit.tsx';

const SETTINGS = '/o/acme/templates/t1?tab=settings';
const form = async () => within(await screen.findByRole('form', { name: 'Template settings' }));

const withSchema = {
  extra_vars_schema: { fields: [{ name: 'region', type: 'string', required: true }, { name: 'token', type: 'secret', required: true }] },
  extra_vars: { region: 'eu', token: { $secret: 'set' } },
};

describe('TemplateSettingsTab', () => {
  it('shows the settings read-only until Edit', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes({ tpl: template({ limit: 'web*' }) }));
    renderTemplatePage(SETTINGS);
    const f = await form();
    expect(f.getByLabelText('Name')).toHaveValue('deploy-site');
    expect(f.getByLabelText('Name')).toBeDisabled();
    expect(f.getByLabelText('Limit pattern')).toHaveValue('web*');
    expect(f.getByLabelText('Limit pattern')).toBeDisabled();
    expect(f.getByRole('switch', { name: 'Enrol agents' })).toBeDisabled();
    expect(f.queryByRole('button', { name: 'Save template' })).not.toBeInTheDocument();
    expect(screen.getByText('The settings as saved. Edit to change them.')).toBeInTheDocument();

    await user.click(within(screen.getByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    const editing = await form();
    expect(editing.getByLabelText('Name')).toBeEnabled();
    expect(editing.getByLabelText('Limit pattern')).toBeEnabled();
    expect(editing.getByRole('button', { name: 'Save template' })).toBeInTheDocument();
    expect(editing.getByRole('button', { name: 'Cancel' })).toBeInTheDocument();
    // Delete is the header's; the variables are a tab of their own.
    expect(editing.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    expect(editing.queryByRole('link', { name: 'Variables' })).not.toBeInTheDocument();
  });

  it('saves under If-Match with the variables echoed, then shows the saved settings', async () => {
    const user = userEvent.setup();
    const puts: MockRequest[] = [];
    let current = template({ revision: 3, ...withSchema });
    mockApi(
      pageRoutes({
        tpl: current,
        over: {
          'GET /api/v1/orgs/acme/job-templates/t1': () => ({ template: current }),
          'PUT /api/v1/orgs/acme/job-templates/t1': (req: MockRequest) => {
            puts.push(req);
            current = template({ revision: 4, ...withSchema, limit: 'web*' });
            return { template: current };
          },
        },
      }),
    );
    renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.type((await form()).getByLabelText('Limit pattern'), 'web*');
    await user.click((await form()).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template deploy-site saved');
    expect(puts).toHaveLength(1);
    expect(puts[0].headers.get('If-Match')).toBe('"3"');
    expect(puts[0].body).toMatchObject({
      name: 'deploy-site',
      limit: 'web*',
      extra_vars: { region: 'eu', token: { $secret: 'keep', from: 'token' } },
      extra_vars_schema: withSchema.extra_vars_schema,
    });
    await waitFor(async () => expect((await form()).getByLabelText('Limit pattern')).toBeDisabled());
    expect((await form()).getByLabelText('Limit pattern')).toHaveValue('web*');
  });

  it('shows the saved settings at once, before the template is read again', async () => {
    const user = userEvent.setup();
    let reads = 0;
    let saved = false;
    mockApi(
      pageRoutes({
        over: {
          // After the save the read again never answers: what shows is the
          // template the save answered with.
          'GET /api/v1/orgs/acme/job-templates/t1': () => (reads++, saved ? new Promise(() => undefined) : { template: template() }),
          'PUT /api/v1/orgs/acme/job-templates/t1': () => ((saved = true), { template: template({ revision: 2, limit: 'web*' }) }),
        },
      }),
    );
    renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.type((await form()).getByLabelText('Limit pattern'), 'web*');
    await user.click((await form()).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template deploy-site saved');
    const f = await form();
    expect(f.getByLabelText('Limit pattern')).toBeDisabled();
    expect(f.getByLabelText('Limit pattern')).toHaveValue('web*');
    expect(reads).toBeGreaterThan(0);
  });

  it("offers Edit once on the Settings tab: the tab's, not the header's", async () => {
    mockApi(pageRoutes());
    renderTemplatePage(SETTINGS);
    await form();
    expect(within(screen.getByRole('tabpanel')).getByRole('button', { name: 'Edit' })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Edit' })).toHaveLength(1);
  });

  it('drops the changes on Cancel', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes());
    renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.type((await form()).getByLabelText('Name'), '-x');
    await user.click((await form()).getByRole('button', { name: 'Cancel' }));
    const f = await form();
    expect(f.getByLabelText('Name')).toBeDisabled();
    expect(f.getByLabelText('Name')).toHaveValue('deploy-site');
  });

  it('asks before another tab discards unsaved changes, and discards them on yes', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes());
    const { router } = renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.type((await form()).getByLabelText('Name'), '-x');
    await user.click(screen.getByRole('tab', { name: /^Overview/ }));
    expect(await acceptConfirm(user)).toMatch(/unsaved changes/i);
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    await user.click(screen.getByRole('tab', { name: /^Settings/ }));
    expect((await form()).getByLabelText('Name')).toBeDisabled();
    expect((await form()).getByLabelText('Name')).toHaveValue('deploy-site');
  });

  it('keeps editing when the question is declined', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes());
    const { router } = renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.type((await form()).getByLabelText('Name'), '-x');
    await user.click(screen.getByRole('tab', { name: /^Runs/ }));
    await declineConfirm(user);
    expect(router.state.location.search).toEqual({ tab: 'settings' });
    expect((await form()).getByLabelText('Name')).toBeEnabled();
    expect((await form()).getByLabelText('Name')).toHaveValue('deploy-site-x');
  });

  it('asks before another page is opened with unsaved changes', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes());
    const { router } = renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.type((await form()).getByLabelText('Limit pattern'), 'web*');
    await user.click(await within(screen.getByTestId('template-facts')).findByRole('link', { name: 'site.yml' }));
    await declineConfirm(user);
    expect(router.state.location.pathname).toBe('/o/acme/templates/t1');
    await user.click(within(screen.getByTestId('template-facts')).getByRole('link', { name: 'site.yml' }));
    await acceptConfirm(user);
    expect(await screen.findByText(/PLAYBOOK PAGE pb1/)).toBeInTheDocument();
  });

  it('leaves an edit without changes, and one after Save, without asking', async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes({ over: { 'PUT /api/v1/orgs/acme/job-templates/t1': { template: template({ revision: 2, limit: 'web*' }) } } }));
    const { router } = renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.click(screen.getByRole('tab', { name: /^Overview/ }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    expect(screen.queryByRole('dialog', { name: 'Are you sure?' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: /^Settings/ }));
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.type((await form()).getByLabelText('Limit pattern'), 'web*');
    await user.click((await form()).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template deploy-site saved');
    await user.click(screen.getByRole('tab', { name: /^Runs/ }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: 'runs' }));
    expect(screen.queryByRole('dialog', { name: 'Are you sure?' })).not.toBeInTheDocument();
  });

  it('offers Reload after a conflict and saves on the new revision, keeping this edit', async () => {
    const user = userEvent.setup();
    const puts: MockRequest[] = [];
    let current = template({ revision: 1 });
    mockApi(
      pageRoutes({
        tpl: current,
        over: {
          'GET /api/v1/orgs/acme/job-templates/t1': () => ({ template: current }),
          'PUT /api/v1/orgs/acme/job-templates/t1': (req: MockRequest) => {
            puts.push(req);
            if (puts.length === 1) {
              // Meanwhile another person narrowed the limit.
              current = template({ revision: 2, limit: 'db*' });
              return new Response(JSON.stringify({ detail: 'stale' }), { status: 412 });
            }
            return { template: current };
          },
        },
      }),
    );
    renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    await user.type((await form()).getByLabelText('Name'), '-2');
    await user.click((await form()).getByRole('button', { name: 'Save template' }));
    expect(await (await form()).findByText(/Someone else saved this template since it was opened/)).toBeInTheDocument();
    await user.click((await form()).getByRole('button', { name: 'Reload' }));
    await waitFor(async () => expect((await form()).getByLabelText('Limit pattern')).toHaveValue('db*'));
    expect((await form()).getByLabelText('Name')).toHaveValue('deploy-site-2');
    expect((await form()).getByLabelText('Name')).toBeEnabled();
    await user.click((await form()).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template deploy-site-2 saved');
    expect(puts[1].headers.get('If-Match')).toBe('"2"');
    expect(puts[1].body).toMatchObject({ name: 'deploy-site-2', limit: 'db*' });
  });

  it("keeps an installation-managed template's own fields read-only while editing", async () => {
    const user = userEvent.setup();
    mockApi(pageRoutes({ tpl: template({ shared_template_id: 'shared-1' }) }));
    renderTemplatePage(SETTINGS);
    await user.click(within(await screen.findByRole('tabpanel')).getByRole('button', { name: 'Edit' }));
    const f = await form();
    expect(f.getByLabelText('Name')).toBeDisabled();
    expect(f.getByLabelText('Limit pattern')).toBeDisabled();
    expect(f.getByRole('button', { name: 'Save template' })).toBeEnabled();
  });

  it('shows a viewer the settings without Edit', async () => {
    mockApi(pageRoutes({ role: 'viewer' }));
    renderTemplatePage(SETTINGS);
    const f = await form();
    expect(f.getByLabelText('Name')).toBeDisabled();
    expect(screen.getByText('The settings as saved. Changing them needs the templates permission.')).toBeInTheDocument();
    expect(within(screen.getByRole('tabpanel')).queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
  });
});
