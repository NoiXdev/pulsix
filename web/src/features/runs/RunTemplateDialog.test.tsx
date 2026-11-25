import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import type { Host } from '../../api/types.ts';
import { RunTemplateDialog } from './RunTemplateDialog.tsx';

const HOST = { id: 'h1', name: 'web-01', zone_id: 'z', address: '', managed: true, monitored: false, facts: {}, created_at: '' } as Host;

const template = (id: string, name: string) => ({
  id, name, playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, created_at: '', updated_at: '',
});

const TEMPLATES = { templates: [template('t1', 'deploy-site'), template('t2', 'patch-all')] };

const job = { id: 'job-9', zone_id: 'z', kind: 'run', status: 'queued', trigger: 'manual', run_group_id: 'g1', created_at: '' };

function problem(status: number, detail: string) {
  return new Response(JSON.stringify({ title: 'Unprocessable Entity', status, detail }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function renderDialog(onClose = () => undefined) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/o/$org',
    component: () => (
      <>
        <RunTemplateDialog org="acme" host={HOST} onClose={onClose} />
        <Outlet />
      </>
    ),
  });
  const runRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([runRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme'] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return router;
}

describe('RunTemplateDialog', () => {
  it('lists the templates, runs the chosen one on this host and opens the run', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    let closed = false;
    mockApi({
      'GET /api/v1/orgs/acme/job-templates': TEMPLATES,
      'POST /api/v1/orgs/acme/job-templates/t2/run': (req: MockRequest) => {
        posted = req.body;
        return new Response(JSON.stringify({ jobs: [job] }), { status: 202, headers: { 'Content-Type': 'application/json' } });
      },
    });
    const router = renderDialog(() => {
      closed = true;
    });

    const dialog = await screen.findByRole('dialog', { name: 'Run template on web-01' });
    const run = within(dialog).getByRole('button', { name: 'Run' });
    expect(run).toBeDisabled();
    await pickOption(within(dialog), 'Template', 'patch-all');
    await user.click(run);

    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
    expect(posted).toEqual({ host_id: 'h1' });
    expect(router.state.location.pathname).toBe('/o/acme/runs/job-9');
    expect(closed).toBe(true);
  });

  it('shows the chosen template\'s follow-ups, preselected, and posts a changed list', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const withFollowUps = { ...template('t1', 'deploy-site'), follow_ups: [{ template_id: 't2', template_name: 'patch-all', position: 0 }] };
    mockApi({
      'GET /api/v1/orgs/acme/job-templates': { templates: [withFollowUps, template('t2', 'patch-all')] },
      'POST /api/v1/orgs/acme/job-templates/t1/run': (req: MockRequest) => {
        posted = req.body;
        return new Response(JSON.stringify({ jobs: [job] }), { status: 202, headers: { 'Content-Type': 'application/json' } });
      },
    });
    renderDialog();

    const dialog = await screen.findByRole('dialog', { name: 'Run template on web-01' });
    expect(within(dialog).queryByLabelText('Follow-ups on success')).not.toBeInTheDocument();
    await pickOption(within(dialog), 'Template', 'deploy-site');
    const list = await within(dialog).findByRole('list', { name: 'Chosen follow-ups' });
    expect(within(list).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['patch-all']);
    await user.click(within(dialog).getByRole('button', { name: 'Remove patch-all' }));
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await screen.findByText('RUN VIEW');
    expect(posted).toEqual({ host_id: 'h1', follow_ups: [] });
  });

  it('shows a refusal inline and stays open, and Run can be pressed again', async () => {
    const user = userEvent.setup();
    let closed = false;
    let posts = 0;
    mockApi({
      'GET /api/v1/orgs/acme/job-templates': TEMPLATES,
      'POST /api/v1/orgs/acme/job-templates/t1/run': () => {
        posts += 1;
        return problem(422, 'host is not among the template targets');
      },
    });
    renderDialog(() => {
      closed = true;
    });

    const dialog = await screen.findByRole('dialog', { name: 'Run template on web-01' });
    await pickOption(within(dialog), 'Template', 'deploy-site');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('host is not among the template targets');
    expect(screen.getByRole('dialog', { name: 'Run template on web-01' })).toBeInTheDocument();
    expect(screen.queryByText('RUN VIEW')).not.toBeInTheDocument();
    expect(closed).toBe(false);

    // A refusal ends the attempt: the lock a successful run keeps is lifted.
    const run = within(dialog).getByRole('button', { name: 'Run' });
    expect(run).toBeEnabled();
    await user.click(run);
    await waitFor(() => expect(posts).toBe(2));
  });

  it('says so and disables Run when the organization has no templates', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/job-templates': { templates: [] } });
    renderDialog();

    const dialog = await screen.findByRole('dialog', { name: 'Run template on web-01' });
    expect(await within(dialog).findByText('No templates yet.')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Run' })).toBeDisabled();
  });

  it('starts one run however often Run is pressed while navigation is pending', async () => {
    const user = userEvent.setup();
    let posts = 0;
    mockApi({
      'GET /api/v1/orgs/acme/job-templates': TEMPLATES,
      'POST /api/v1/orgs/acme/job-templates/t1/run': () => {
        posts += 1;
        return new Response(JSON.stringify({ jobs: [job] }), { status: 202, headers: { 'Content-Type': 'application/json' } });
      },
    });
    // onClose is a no-op, as it is while the route change is still pending:
    // the dialog stays mounted with an enabled Run button.
    renderDialog();

    const dialog = await screen.findByRole('dialog', { name: 'Run template on web-01' });
    await pickOption(within(dialog), 'Template', 'deploy-site');
    const run = within(dialog).getByRole('button', { name: 'Run' });
    await user.click(run);
    await user.click(run);
    await user.click(run);

    await screen.findByText('RUN VIEW');
    expect(posts).toBe(1);
  });

  it('clears an earlier refusal once another template is chosen', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/job-templates': TEMPLATES,
      'POST /api/v1/orgs/acme/job-templates/t1/run': () => problem(422, 'host is not among the template targets'),
    });
    renderDialog();

    const dialog = await screen.findByRole('dialog', { name: 'Run template on web-01' });
    await pickOption(within(dialog), 'Template', 'deploy-site');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('host is not among the template targets');

    await pickOption(within(dialog), 'Template', 'patch-all');

    expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument();
  });

  // Spec 2026-11-25 B, controller ruling C: a template with agent
  // enrollment asks first whether this host gets an agent, and sends the
  // confirmed hosts; cancelling starts nothing.
  it('asks to confirm the agent enrollment of a template that enrols agents, and sends the confirmed hosts', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    let previewQuery = '';
    const enrolling = { ...template('t1', 'install-agent'), agent_enrollment: true };
    mockApi({
      'GET /api/v1/orgs/acme/job-templates': { templates: [enrolling] },
      'GET /api/v1/orgs/acme/job-templates/t1/enrollment-preview': (req: MockRequest) => {
        previewQuery = req.search.get('host_id') ?? '';
        return { enrols: true, hosts: [{ id: 'h1', name: 'web-01' }] };
      },
      'POST /api/v1/orgs/acme/job-templates/t1/run': (req: MockRequest) => {
        posted = req.body;
        return new Response(JSON.stringify({ jobs: [job] }), { status: 202, headers: { 'Content-Type': 'application/json' } });
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Run template on web-01' });
    await pickOption(within(dialog), 'Template', 'install-agent');

    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    const ask = await screen.findByRole('dialog', { name: 'Enrol agents' });
    expect(ask).toHaveTextContent('Will enrol agents on: web-01.');
    await user.click(within(ask).getByRole('button', { name: 'Cancel' }));
    expect(posted).toBeUndefined();

    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Enrol agents' })).getByRole('button', { name: 'Start and enrol' }));
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
    expect(previewQuery).toBe('h1');
    expect(posted).toEqual({ host_id: 'h1', enroll_host_ids: ['h1'] });
  });
});
