import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { SharedResourcesPage } from './SharedResourcesPage.tsx';

const ADMIN = {
  user: { id: 'u1', email: 'admin@acme.io', display_name: 'Ada' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: true,
};

const def = (over: Record<string, unknown> = {}) => ({
  id: 'd1', name: 'ansible-base', url: 'https://git.example/provider/ansible-base.git', branch: 'main', subpath: '', ...over,
});

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const sharedRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/shared-resources', component: SharedResourcesPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([sharedRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/shared-resources'] }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

// v0.2f Task 5: the instance-admin screen that creates, edits and deletes
// repository definitions (spec decisions 1-8) — the installation-owned side
// of what ReposPage.tsx's RepoDialog shows read-only inside an organization.
describe('SharedResourcesPage', () => {
  it('lists repository definitions and shows the Shared resources tab', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
    });
    renderPage();
    expect(await screen.findByText('ansible-base')).toBeInTheDocument();
    expect(screen.getByText('https://git.example/provider/ansible-base.git')).toBeInTheDocument();
    expect(await screen.findByRole('tab', { name: 'Shared resources' })).toBeInTheDocument();
  });

  it('creates a repository definition, projected into every organization by the server', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const definitions: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': () => ({ definitions }),
      'POST /api/v1/admin/repos/definitions': (req: MockRequest) => {
        posted = req.body;
        const created = def({ id: 'd9', ...(req.body as Record<string, unknown>) });
        definitions.push(created);
        return { definition: created };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository definition' }));
    await user.type(screen.getByLabelText('Name'), 'infra-base');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/provider/infra-base.git');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));

    expect(await screen.findByText('infra-base')).toBeInTheDocument();
    expect(posted).toMatchObject({ name: 'infra-base', url: 'https://git.example/provider/infra-base.git' });
    expect(await screen.findByText('Repository definition infra-base created')).toBeInTheDocument();
  });

  it('does not submit while the required fields are empty', async () => {
    const user = userEvent.setup();
    let posts = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'POST /api/v1/admin/repos/definitions': () => {
        posts += 1;
        return { definition: def() };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository definition' }));
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findAllByText('required')).toHaveLength(2);
    expect(posts).toBe(0);
  });

  it('edits a definition, propagating its branch to every projection', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
      'PUT /api/v1/admin/repos/definitions/d1': (req: MockRequest) => {
        put = req.body;
        return { definition: def({ branch: 'release' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'ansible-base' }));
    const dialog = await screen.findByRole('dialog', { name: 'ansible-base' });
    const branch = within(dialog).getByLabelText('Branch');
    await user.clear(branch);
    await user.type(branch, 'release');
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(put).toEqual({
      name: 'ansible-base', url: 'https://git.example/provider/ansible-base.git', branch: 'release', subpath: '',
    });
    expect(await screen.findByText('Repository definition ansible-base saved')).toBeInTheDocument();
  });

  // Spec decision 7: a name collision names the colliding organizations —
  // to an instance admin only, which this screen is. The server composes
  // the full sentence (mapSharedErr, internal/api/shared_handlers.go)
  // specifically so this is the one screen that can show it whole.
  it('shows which organizations already use a name when creating a definition collides', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'POST /api/v1/admin/repos/definitions': () =>
        new Response(JSON.stringify({ detail: 'name "infra" is already used by acme, widget-co' }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository definition' }));
    await user.type(screen.getByLabelText('Name'), 'infra');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/provider/infra.git');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name "infra" is already used by acme, widget-co');
  });

  // Spec decision 5: a definition a customer's schedule, trigger or
  // playbook still references refuses to delete, naming the organization
  // and what still references it.
  it('shows which organization and what blocks a delete', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
      'DELETE /api/v1/admin/repos/definitions/d1': () =>
        new Response(
          JSON.stringify({ detail: "organization acme: job triggers still reference this definition's projection" }),
          { status: 409, headers: { 'Content-Type': 'application/json' } },
        ),
    });
    renderPage();
    await screen.findByText('ansible-base');
    await user.click(screen.getByRole('button', { name: 'Delete ansible-base' }));
    expect(await screen.findByText("organization acme: job triggers still reference this definition's projection")).toBeInTheDocument();
    // Refused, not removed: the row is still there.
    expect(screen.getByText('ansible-base')).toBeInTheDocument();
  });

  it('deletes a definition that nothing references', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': () => ({ definitions: deleted ? [] : [def()] }),
      'DELETE /api/v1/admin/repos/definitions/d1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Delete ansible-base' }));
    expect(await screen.findByText('No shared repository definitions yet.')).toBeInTheDocument();
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
    });
    renderPage();
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});
