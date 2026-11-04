import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../../test/mockApi.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedTemplatesPage } from './SharedTemplatesPage.tsx';

const repoDef = () => ({
  id: 'r1', name: 'infra-base', url: 'https://git.example/provider/infra-base.git', branch: 'main', subpath: '',
});
const tplDef = (over: Record<string, unknown> = {}) => ({
  id: 't1', name: 'deploy-site', shared_repo_id: 'r1', playbook_path: 'site.yml', limit: '', extra_vars: {}, ...over,
});

function renderPage() {
  return renderRoutes(
    [
      { path: '/settings/shared/templates', component: SharedTemplatesPage },
      { path: '/settings/shared/templates/new', component: () => <div>new page</div> },
      { path: '/settings/shared/templates/$id', component: () => <div>detail page</div> },
    ],
    '/o/acme/settings/shared/templates',
  );
}

afterEach(() => vi.restoreAllMocks());

describe('SharedTemplatesPage', () => {
  it('lists job template definitions, naming the repository they resolve against', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef({ limit: 'web*' })] },
    });
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Shared job templates', level: 1 })).toBeInTheDocument();
    // The repository column resolves the id to the repository's own name.
    expect(await screen.findByText('infra-base')).toBeInTheDocument();
    expect(screen.getByText('site.yml')).toBeInTheDocument();
    expect(screen.getByText('web*')).toBeInTheDocument();
  });

  it('makes a row a link to its form page and links Add to the new page', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef()] },
    });
    renderPage();
    expect(await screen.findByRole('link', { name: 'deploy-site' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/templates/t1',
    );
    expect(screen.getByRole('link', { name: 'Add job template definition' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/templates/new',
    );
  });

  it('offers Open and Delete… in the row menu, and Open goes to the form page', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef()] },
    });
    const router = renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for deploy-site' }));
    expect(screen.getByRole('menuitem', { name: 'Delete…' })).toBeInTheDocument();
    await user.click(screen.getByRole('menuitem', { name: 'Open' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/templates/t1'));
  });

  it('deletes a definition after confirming', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': () => ({ definitions: deleted ? [] : [tplDef()] }),
      'DELETE /api/v1/admin/templates/definitions/t1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for deploy-site' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    expect(confirm).toHaveBeenCalledWith(
      'Delete job template definition deploy-site? This removes it from every organization.',
    );
    expect(await screen.findByText('No shared job template definitions yet.')).toBeInTheDocument();
  });

  it('shows which organization and what blocks a delete', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef()] },
      'DELETE /api/v1/admin/templates/definitions/t1': () =>
        new Response(JSON.stringify({ detail: "organization acme: a schedule still references this definition's projection" }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for deploy-site' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    expect(
      await screen.findByText("organization acme: a schedule still references this definition's projection"),
    ).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'deploy-site' })).toBeInTheDocument();
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    const denied = () => new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 });
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': denied,
      'GET /api/v1/admin/templates/definitions': denied,
    });
    renderPage();
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});
