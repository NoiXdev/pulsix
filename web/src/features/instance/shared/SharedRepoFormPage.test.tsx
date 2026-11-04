import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedRepoFormPage } from './SharedRepoFormPage.tsx';

const LIST = '/o/acme/settings/shared/repos';

const def = (over: Record<string, unknown> = {}) => ({
  id: 'd1', name: 'ansible-base', url: 'https://git.example/provider/ansible-base.git', branch: 'main', subpath: '', ...over,
});

function renderPage(path: string) {
  return renderRoutes(
    [
      { path: '/settings/shared/repos', component: () => <div>the list</div> },
      { path: '/settings/shared/repos/new', component: SharedRepoFormPage },
      { path: '/settings/shared/repos/$id', component: SharedRepoFormPage },
    ],
    path,
  );
}

afterEach(() => vi.restoreAllMocks());

describe('SharedRepoFormPage — new', () => {
  it('posts a new definition and goes back to the list', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'POST /api/v1/admin/repos/definitions': (req: MockRequest) => {
        posted = req.body;
        return { definition: def({ id: 'd9', ...(req.body as Record<string, unknown>) }) };
      },
    });
    const router = renderPage(`${LIST}/new`);
    expect(await screen.findByRole('heading', { name: 'New repository definition', level: 1 })).toBeInTheDocument();
    await user.type(screen.getByLabelText('Name'), 'infra-base');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/provider/infra-base.git');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));

    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
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
    const router = renderPage(`${LIST}/new`);
    await user.click(await screen.findByRole('button', { name: 'Create definition' }));
    expect(await screen.findAllByText('required')).toHaveLength(2);
    expect(posts).toBe(0);
    expect(router.state.location.pathname).toBe(`${LIST}/new`);
  });

  // Spec decision 7: a name collision names the colliding organizations —
  // to an instance admin only, which this screen is. The server composes
  // the full sentence (mapSharedErr, internal/api/shared_handlers.go)
  // specifically so this is the one screen that can show it whole.
  it('shows which organizations already use a name when the create collides, and stays', async () => {
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
    const router = renderPage(`${LIST}/new`);
    await user.type(await screen.findByLabelText('Name'), 'infra');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/provider/infra.git');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name "infra" is already used by acme, widget-co');
    expect(router.state.location.pathname).toBe(`${LIST}/new`);
  });

  it('asks before leaving with unsaved changes, and stays when declined', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
    });
    const router = renderPage(`${LIST}/new`);
    await user.type(await screen.findByLabelText('Name'), 'infra');
    await user.click(screen.getByRole('link', { name: 'Cancel' }));
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(router.state.location.pathname).toBe(`${LIST}/new`);
  });

  it('leaves without asking while nothing has been typed', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
    });
    const router = renderPage(`${LIST}/new`);
    await user.click(await screen.findByRole('link', { name: 'Cancel' }));
    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
    expect(confirm).not.toHaveBeenCalled();
  });
});

describe('SharedRepoFormPage — edit', () => {
  it('loads the definition, puts the change and goes back to the list', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    let put: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
      'PUT /api/v1/admin/repos/definitions/d1': (req: MockRequest) => {
        put = req.body;
        return { definition: def({ branch: 'release' }) };
      },
    });
    const router = renderPage(`${LIST}/d1`);
    expect(await screen.findByRole('heading', { name: 'ansible-base', level: 1 })).toBeInTheDocument();
    expect(screen.getByLabelText('Name')).toHaveValue('ansible-base');
    const branch = screen.getByLabelText('Branch');
    await user.clear(branch);
    await user.type(branch, 'release');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));

    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
    expect(put).toEqual({
      name: 'ansible-base', url: 'https://git.example/provider/ansible-base.git', branch: 'release', subpath: '',
    });
    expect(await screen.findByText('Repository definition ansible-base saved')).toBeInTheDocument();
    // The save's own navigation is not an unsaved-changes departure.
    expect(confirm).not.toHaveBeenCalled();
  });

  it('shows the not-found page for an id that does not exist', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
    });
    renderPage(`${LIST}/nope`);
    expect(await screen.findByText('Not found')).toBeInTheDocument();
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
    });
    renderPage(`${LIST}/d1`);
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});
