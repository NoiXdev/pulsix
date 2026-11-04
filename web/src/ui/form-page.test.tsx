import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { FormPage } from './form-page.tsx';

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
  render(<RouterProvider router={router} />);
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
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const onSubmit = vi.fn().mockResolvedValue(true);
    const router = renderPage({ dirty: true, onSubmit });
    await user.click(await screen.findByRole('button', { name: 'Save' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos'));
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(confirm).not.toHaveBeenCalled();
  });

  it('stays on the page when onSubmit reports nothing was saved', async () => {
    const user = userEvent.setup();
    const router = renderPage({ dirty: true, onSubmit: () => false });
    await user.click(await screen.findByRole('button', { name: 'Save' }));
    expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos/new');
  });

  it('leaves without asking when nothing changed', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const router = renderPage();
    await user.click(await screen.findByRole('link', { name: 'Cancel' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos'));
    expect(confirm).not.toHaveBeenCalled();
  });

  it('asks before leaving with unsaved changes, and stays when declined', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const router = renderPage({ dirty: true });
    await user.click(await screen.findByRole('link', { name: 'Cancel' }));
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(confirm).toHaveBeenCalledWith('Leave this page? Your unsaved changes will be lost.');
    expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos/new');
  });

  it('leaves with unsaved changes once the confirmation is accepted', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const router = renderPage({ dirty: true });
    await user.click(await screen.findByRole('link', { name: 'Cancel' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/o/acme/settings/shared/repos'));
  });
});
