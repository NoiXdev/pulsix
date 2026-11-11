import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ConfirmProvider } from './confirm.tsx';
import { FormPage } from './form-page.tsx';
import { settled } from '../test/settled.ts';

type Props = Partial<Parameters<typeof FormPage>[0]>;

function renderPage(props: Props = {}) {
  const rootRoute = createRootRoute();
  const list = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org/settings/shared/repos', component: () => <div>the list</div> });
  const form = createRoute({
    getParentRoute: () => rootRoute,
    path: '/o/$org/settings/shared/repos/new',
    component: () => (
      <FormPage
        title="New thing"
        backTo={{ to: '/o/$org/settings/shared/repos', params: { org: 'acme' } }}
        onSubmit={() => true}
        submitting={false}
        dirty={false}
        {...props}
      >
        <label>
          Name
          <input />
        </label>
      </FormPage>
    ),
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([list, form]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/shared/repos/new'] }),
  });
  render(
    <ConfirmProvider>
      <RouterProvider router={router} />
    </ConfirmProvider>,
  );
  return router;
}

afterEach(() => vi.restoreAllMocks());

describe('FormPage', () => {
  it('shows the title, the fields and a footer with Save and Cancel', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { name: 'New thing', level: 1 })).toBeInTheDocument();
    expect(screen.getByLabelText('Name')).toBeInTheDocument();
    const footer = screen.getByRole('group', { name: 'Form actions' });
    expect(footer).toBeInTheDocument();
    expect(footer).toContainElement(screen.getByRole('button', { name: 'Save' }));
    expect(footer).toContainElement(screen.getByRole('link', { name: 'Cancel' }));
  });

  it('labels the submit button with submitLabel when given', async () => {
    renderPage({ submitLabel: 'Create definition' });
    expect(await screen.findByRole('button', { name: 'Create definition' })).toBeInTheDocument();
  });

  it('disables Save while submitting', async () => {
    renderPage({ submitting: true });
    expect(await screen.findByRole('button', { name: 'Save' })).toBeDisabled();
  });

  it('leaves for the list without asking once onSubmit reports a save, even with unsaved changes', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(true);
    const router = renderPage({ dirty: true, onSubmit });
    await user.click(await screen.findByRole('button', { name: 'Save' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos'));
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(document.querySelector('dialog[open]')).toBeNull();
  });

  it('stays on the page when onSubmit reports nothing was saved', async () => {
    const user = userEvent.setup();
    const router = renderPage({ dirty: true, onSubmit: () => false });
    await user.click(await screen.findByRole('button', { name: 'Save' }));
    expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos/new');
  });

  it('leaves without asking when nothing changed', async () => {
    const user = userEvent.setup();
    const router = renderPage();
    await user.click(await screen.findByRole('link', { name: 'Cancel' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos'));
    expect(document.querySelector('dialog[open]')).toBeNull();
  });

  it('asks before leaving with unsaved changes, and stays when declined', async () => {
    const user = userEvent.setup();
    const router = renderPage({ dirty: true });
    await user.click(await screen.findByRole('link', { name: 'Cancel' }));
    const dialog = await screen.findByRole('dialog', { name: 'Are you sure?' });
    expect(dialog).toHaveTextContent('Leave this page? Your unsaved changes will be lost.');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await settled();
    expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos/new');
  });

  it('leaves with unsaved changes once the confirmation is accepted', async () => {
    const user = userEvent.setup();
    const router = renderPage({ dirty: true });
    await user.click(await screen.findByRole('link', { name: 'Cancel' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Are you sure?' })).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos'));
  });
});
