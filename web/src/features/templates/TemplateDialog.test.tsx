import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { TemplateDialog } from './TemplateDialog.tsx';

const playbookRow = { id: 'pb1', source: 'editor', summary: 'Install nginx', created_at: '', updated_at: '' };

const template = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id, name, playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, revision: 1,
  vars_mode: 'off', created_at: '', updated_at: '', follow_ups: [], ...over,
});

const A = template('a', 'alpha');
const B = template('b', 'beta');
const C = template('c', 'gamma');

function problem422(location: string, message: string) {
  return new Response(JSON.stringify({ title: 'Unprocessable Entity', status: 422, detail: message, errors: [{ location, message }] }), {
    status: 422,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

function renderDialog(id: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({
    component: () => (
      <TemplateDialog org="acme" template={{ id, name: 'name' }} canReadCredentials={false} canCreateCredential={false} onClose={() => undefined} />
    ),
  });
  const router = createRouter({ routeTree: rootRoute, history: createMemoryHistory({ initialEntries: ['/'] }) });
  render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

function routes(current: ReturnType<typeof template>, onPut: (req: MockRequest) => unknown) {
  return {
    'GET /api/v1/orgs/acme/job-templates': { templates: [A, B, C] },
    [`GET /api/v1/orgs/acme/job-templates/${current.id}`]: { template: current },
    'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
    'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
    [`PUT /api/v1/orgs/acme/job-templates/${current.id}`]: onPut,
  };
}

describe('TemplateDialog follow-ups', () => {
  it('shows the stored follow-ups in order, offers the others but not itself, and sends the list', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    const loaded = template('a', 'alpha', {
      follow_ups: [
        { template_id: 'c', template_name: 'gamma', position: 1 },
        { template_id: 'b', template_name: 'beta', position: 0 },
      ],
    });
    mockApi(routes(loaded, (req) => ((put = req), { template: loaded })));
    renderDialog('a');
    const dialog = await screen.findByRole('dialog', { name: 'alpha' });
    const list = await within(dialog).findByRole('list', { name: 'Chosen follow-ups' });
    expect(within(list).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['beta', 'gamma']);
    await user.click(within(dialog).getByLabelText('Follow-ups on success'));
    expect(screen.queryByRole('option', { name: 'alpha' })).not.toBeInTheDocument();
    await user.keyboard('{Escape}');
    await user.click(within(dialog).getByRole('button', { name: 'Remove beta' }));
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template alpha saved');
    expect(put?.body).toMatchObject({ follow_ups: [{ template_id: 'c' }] });
  });

  it('sends an empty list when none is chosen, so a cleared list is cleared', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    mockApi(routes(A, (req) => ((put = req), { template: A })));
    renderDialog('a');
    const dialog = await screen.findByRole('dialog', { name: 'alpha' });
    await user.click(await within(dialog).findByRole('button', { name: 'Save template' }));
    await screen.findByText('Template alpha saved');
    expect((put?.body as { follow_ups: unknown }).follow_ups).toEqual([]);
  });

  it('shows the cycle refusal under the field', async () => {
    const user = userEvent.setup();
    mockApi(routes(A, () => problem422('body.follow_ups', 'follow-ups would form a cycle: alpha → beta → alpha')));
    renderDialog('a');
    const dialog = await screen.findByRole('dialog', { name: 'alpha' });
    await pickOption(within(dialog), 'Follow-ups on success', 'beta');
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    const alert = await within(dialog).findByText('follow-ups would form a cycle: alpha → beta → alpha');
    expect(alert).toHaveAttribute('role', 'alert');
    expect(within(dialog).getByLabelText('Follow-ups on success')).toHaveAttribute('aria-describedby', expect.stringContaining('template-follow-ups-error'));
  });

  it('shows the refusal of a follow-up deleted meanwhile under the field', async () => {
    const user = userEvent.setup();
    const loaded = template('a', 'alpha', { follow_ups: [{ template_id: 'gone', template_name: 'gone', position: 0 }] });
    mockApi(routes(loaded, () => problem422('body.follow_ups', 'invalid input: follow-up template does not exist')));
    renderDialog('a');
    const dialog = await screen.findByRole('dialog', { name: 'alpha' });
    await within(dialog).findByText('gone (unavailable)');
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    const alert = await within(dialog).findByText('invalid input: follow-up template does not exist');
    expect(alert).toHaveAttribute('role', 'alert');
  });

  it('keeps a stored follow-up the template list does not hold, listed as unavailable, and sends it', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    const loaded = template('a', 'alpha', { follow_ups: [{ template_id: 'gone', template_name: 'gone', position: 0 }] });
    mockApi(routes(loaded, (req) => ((put = req), { template: loaded })));
    renderDialog('a');
    const dialog = await screen.findByRole('dialog', { name: 'alpha' });
    expect(await within(dialog).findByText('gone (unavailable)')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Save template' }));
    await screen.findByText('Template alpha saved');
    expect(put?.body).toMatchObject({ follow_ups: [{ template_id: 'gone' }] });
  });
});
