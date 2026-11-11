import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedRepoFormPage } from './SharedRepoFormPage.tsx';
import { settled } from '../../../test/settled.ts';

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

  // Backlog 7.94: a definition's subpath follows an organization's
  // repository's rule, so the form says so before the server refuses it.
  it('does not submit a subpath with ".." and names the rule', async () => {
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
    renderPage(`${LIST}/new`);
    await user.type(await screen.findByLabelText('Name'), 'infra-base');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/provider/infra-base.git');
    await user.type(screen.getByLabelText('Subdirectory'), '../outside');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByText(/^invalid subpath: may be empty/)).toBeInTheDocument();
    expect(posts).toBe(0);
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
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
    });
    const router = renderPage(`${LIST}/new`);
    await user.type(await screen.findByLabelText('Name'), 'infra');
    await user.click(screen.getByRole('link', { name: 'Cancel' }));
    const dialog = await screen.findByRole('dialog', { name: 'Are you sure?' });
    expect(dialog).toHaveTextContent('Leave this page?');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await settled();
    expect(router.state.location.pathname).toBe(`${LIST}/new`);
  });

  it('leaves without asking while nothing has been typed', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
    });
    const router = renderPage(`${LIST}/new`);
    await user.click(await screen.findByRole('link', { name: 'Cancel' }));
    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
    expect(document.querySelector('dialog[open]')).toBeNull();
  });
});

describe('SharedRepoFormPage — access', () => {
  // The new page used to render the whole form for anyone and only failed
  // with a 403 on save; it now shows the same access-denied state as the
  // list and the edit page.
  it('shows the access-denied page on the new page to someone who is not an instance administrator', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
    expect(screen.queryByLabelText('Name')).not.toBeInTheDocument();
  });
});

describe('SharedRepoFormPage — edit', () => {
  it('loads the definition, puts the change and goes back to the list', async () => {
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
    expect(document.querySelector('dialog[open]')).toBeNull();
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

describe('SharedRepoFormPage — create gate', () => {
  // The create page needs no definitions list: it only checks that the
  // account may create one, from /auth/me, so a failing list cannot block it.
  it('shows the empty form without requesting the definitions list', async () => {
    let listed = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': () => {
        listed += 1;
        return new Response(JSON.stringify({ detail: 'boom' }), { status: 500 });
      },
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByRole('heading', { name: 'New repository definition', level: 1 })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create definition' })).toBeInTheDocument();
    expect(listed).toBe(0);
  });

  it('shows the access-denied page to a non-admin without requesting the definitions list', async () => {
    let listed = 0;
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': () => {
        listed += 1;
        return { definitions: [] };
      },
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Create definition' })).not.toBeInTheDocument();
    expect(listed).toBe(0);
  });
});
