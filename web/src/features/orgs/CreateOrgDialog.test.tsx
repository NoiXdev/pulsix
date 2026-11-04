import { describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { CreateOrgDialog } from './CreateOrgDialog.tsx';

async function render(onClose = vi.fn()) {
  const router = renderRoutes(
    [
      { path: '/settings/organizations', component: () => <CreateOrgDialog open onClose={onClose} /> },
      { path: '/hosts', component: () => <div>hosts page</div> },
    ],
    '/o/acme/settings/organizations',
  );
  await screen.findByLabelText('Name');
  return { router, onClose };
}

const created = { org: { id: 'o2', slug: 'globex', name: 'Globex', role: 'owner', created_at: '2026-11-04T10:00:00Z' } };

describe('CreateOrgDialog', () => {
  it('derives the slug from the name when the slug field is tabbed past empty', async () => {
    const user = userEvent.setup();
    await render();

    await user.type(screen.getByLabelText('Name'), 'Globex Corp');
    // SlugField fills itself on its own blur: the first tab lands on it, the second leaves it.
    await user.tab();
    await user.tab();

    expect(screen.getByLabelText('Slug')).toHaveValue('globex-corp');
  });

  it('does not overwrite a slug that was typed', async () => {
    const user = userEvent.setup();
    await render();

    await user.type(screen.getByLabelText('Slug'), 'gx');
    await user.type(screen.getByLabelText('Name'), 'Globex Corp');
    await user.tab();
    await user.tab();

    expect(screen.getByLabelText('Slug')).toHaveValue('gx');
  });

  it('posts the organization, closes and opens its hosts page', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'POST /api/v1/orgs': (req: MockRequest) => {
        posted = req.body;
        return new Response(JSON.stringify(created), { status: 201 });
      },
    });
    const { router, onClose } = await render();

    await user.type(screen.getByLabelText('Name'), 'Globex');
    await user.type(screen.getByLabelText('Slug'), 'globex');
    await user.click(screen.getByRole('button', { name: 'Create organization' }));

    expect(await screen.findByText('hosts page')).toBeInTheDocument();
    expect(posted).toEqual({ name: 'Globex', slug: 'globex' });
    expect(router.state.location.pathname).toBe('/o/globex/hosts');
    expect(onClose).toHaveBeenCalled();
  });

  it('shows a taken slug at the slug field and keeps the dialog open', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs': () =>
        new Response(JSON.stringify({ title: 'Conflict', status: 409, detail: 'slug already in use' }), { status: 409 }),
    });
    const { router, onClose } = await render();

    await user.type(screen.getByLabelText('Name'), 'Globex');
    await user.type(screen.getByLabelText('Slug'), 'acme');
    await user.click(screen.getByRole('button', { name: 'Create organization' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('That slug is already taken');
    expect(screen.getByLabelText('Slug')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByLabelText('Slug')).toHaveAccessibleDescription(/already taken/);
    expect(onClose).not.toHaveBeenCalled();
    expect(router.state.location.pathname).toBe('/o/acme/settings/organizations');
  });

  it('refuses an empty form and a malformed slug without a request', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'POST /api/v1/orgs': (req: MockRequest) => {
        posted.push(req.body);
        return new Response(JSON.stringify(created), { status: 201 });
      },
    });
    await render();

    await user.click(screen.getByRole('button', { name: 'Create organization' }));
    expect((await screen.findAllByText('required')).length).toBe(2);

    await user.type(screen.getByLabelText('Name'), 'Globex');
    await user.type(screen.getByLabelText('Slug'), 'Not A Slug');
    await user.click(screen.getByRole('button', { name: 'Create organization' }));
    await waitFor(() => expect(screen.queryByText('required')).not.toBeInTheDocument());
    expect(posted).toEqual([]);
  });

  it('says so when the request fails for another reason', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs': () =>
        new Response(JSON.stringify({ title: 'Forbidden', status: 403, detail: 'instance admin required' }), { status: 403 }),
    });
    await render();

    await user.type(screen.getByLabelText('Name'), 'Globex');
    await user.type(screen.getByLabelText('Slug'), 'globex');
    await user.click(screen.getByRole('button', { name: 'Create organization' }));

    expect(await screen.findByRole('status')).toHaveTextContent('instance admin required');
  });
});
