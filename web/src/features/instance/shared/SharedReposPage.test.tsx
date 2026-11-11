import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../../test/mockApi.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedReposPage } from './SharedReposPage.tsx';
import { acceptConfirm, declineConfirm } from '../../../test/confirmDialog.ts';
import { settled } from '../../../test/settled.ts';

const def = (over: Record<string, unknown> = {}) => ({
  id: 'd1', name: 'ansible-base', url: 'https://git.example/provider/ansible-base.git', branch: 'main', subpath: '', ...over,
});

function renderPage() {
  return renderRoutes(
    [
      { path: '/settings/shared/repos', component: SharedReposPage },
      { path: '/settings/shared/repos/new', component: () => <div>new page</div> },
      { path: '/settings/shared/repos/$id', component: () => <div>detail page</div> },
    ],
    '/o/acme/settings/shared/repos',
  );
}

afterEach(() => vi.restoreAllMocks());

describe('SharedReposPage', () => {
  it('lists repository definitions under the Shared repositories title', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
    });
    renderPage();
    expect(await screen.findByText('https://git.example/provider/ansible-base.git')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Shared repositories', level: 1 })).toBeInTheDocument();
    expect(screen.getByText(/^1 repository definitions/)).toBeInTheDocument();
  });

  it('makes a row a link to its form page', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
    });
    renderPage();
    expect(await screen.findByRole('link', { name: 'ansible-base' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/repos/d1',
    );
  });

  it('links Add to the new-definition page', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
    });
    renderPage();
    expect(await screen.findByRole('link', { name: 'Add repository definition' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/repos/new',
    );
    expect(screen.getByText('No shared repository definitions yet.')).toBeInTheDocument();
  });

  it('offers Open and Delete… in the row menu, and Open goes to the form page', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
    });
    const router = renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for ansible-base' }));
    expect(screen.getByRole('menuitem', { name: 'Delete…' })).toBeInTheDocument();
    await user.click(screen.getByRole('menuitem', { name: 'Open' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos/d1'));
  });

  it('deletes a definition after confirming', async () => {
    const user = userEvent.setup();
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
    await user.click(await screen.findByRole('button', { name: 'Actions for ansible-base' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    expect(await acceptConfirm(user)).toBe(
      'Delete repository definition ansible-base? This removes it from every organization.',
    );
    expect(await screen.findByText('No shared repository definitions yet.')).toBeInTheDocument();
    expect(await screen.findByText('Repository definition ansible-base deleted')).toBeInTheDocument();
  });

  it('does not delete when the confirmation is declined', async () => {
    const user = userEvent.setup();
    let deletes = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
      'DELETE /api/v1/admin/repos/definitions/d1': () => {
        deletes += 1;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for ansible-base' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await declineConfirm(user);
    await settled();
    expect(deletes).toBe(0);
    expect(screen.getByRole('link', { name: 'ansible-base' })).toBeInTheDocument();
  });

  // Spec decision 5: a definition a customer's schedule, trigger or
  // playbook still references refuses to delete, naming the organization
  // and what still references it.
  it('shows which organization and what blocks a delete', async () => {
    const user = userEvent.setup();
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
    await user.click(await screen.findByRole('button', { name: 'Actions for ansible-base' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await acceptConfirm(user);
    expect(
      await screen.findByText("organization acme: job triggers still reference this definition's projection"),
    ).toBeInTheDocument();
    // Refused, not removed: the row is still there.
    expect(screen.getByRole('link', { name: 'ansible-base' })).toBeInTheDocument();
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
