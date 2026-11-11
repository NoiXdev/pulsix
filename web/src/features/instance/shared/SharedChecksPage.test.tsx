import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../../test/mockApi.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedChecksPage } from './SharedChecksPage.tsx';
import { acceptConfirm } from '../../../test/confirmDialog.ts';

const checkDef = (over: Record<string, unknown> = {}) => ({
  id: 'c1', key: 'disk-data', plugin: 'disk', params: {}, interval_seconds: 300, ...over,
});

function renderPage() {
  return renderRoutes(
    [
      { path: '/settings/shared/checks', component: SharedChecksPage },
      { path: '/settings/shared/checks/new', component: () => <div>new page</div> },
      { path: '/settings/shared/checks/$id', component: () => <div>detail page</div> },
    ],
    '/o/acme/settings/shared/checks',
  );
}

afterEach(() => vi.restoreAllMocks());

describe('SharedChecksPage', () => {
  it('lists check definitions', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [checkDef()] },
    });
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Shared check definitions', level: 1 })).toBeInTheDocument();
    expect(await screen.findByRole('link', { name: 'disk-data' })).toBeInTheDocument();
    expect(screen.getByText('disk')).toBeInTheDocument();
    expect(screen.getByText('300s')).toBeInTheDocument();
  });

  it('makes a row a link to its form page and links Add to the new page', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [checkDef()] },
    });
    renderPage();
    expect(await screen.findByRole('link', { name: 'disk-data' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/checks/c1',
    );
    expect(screen.getByRole('link', { name: 'Add check definition' })).toHaveAttribute(
      'href',
      '/o/acme/settings/shared/checks/new',
    );
  });

  it('offers Open and Delete… in the row menu, and Open goes to the form page', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [checkDef()] },
    });
    const router = renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for disk-data' }));
    expect(screen.getByRole('menuitem', { name: 'Delete…' })).toBeInTheDocument();
    await user.click(screen.getByRole('menuitem', { name: 'Open' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/checks/c1'));
  });

  // Nothing in the schema can ever block a check definition's delete (spec
  // decision 5 has no dependant for this kind), so there is no 409 case.
  it('deletes a definition after confirming', async () => {
    const user = userEvent.setup();
    let deleted = false;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': () => ({ definitions: deleted ? [] : [checkDef()] }),
      'DELETE /api/v1/admin/checks/definitions/c1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for disk-data' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    expect(await acceptConfirm(user)).toBe(
      'Delete check definition disk-data? This removes it from every organization.',
    );
    expect(await screen.findByText('No shared check definitions yet.')).toBeInTheDocument();
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/checks/definitions': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
    });
    renderPage();
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});
