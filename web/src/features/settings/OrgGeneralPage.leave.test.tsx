import { describe, expect, it } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';
import { OrgGeneralPage } from './OrgGeneralPage.tsx';

const LEAVE = 'Leave this page? Your unsaved changes will be lost.';

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const generalRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/general', component: OrgGeneralPage });
  const membersRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/members', component: () => <p>Members page</p> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([generalRoute, membersRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/general'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <ConfirmProvider>
          <RouterProvider router={router} />
        </ConfirmProvider>
      </ToastProvider>
    </QueryClientProvider>,
  );
  const toMembers = () => act(() => void router.navigate({ to: '/o/$org/settings/members', params: { org: 'acme' } }));
  return { router, toMembers };
}

const org = (name = 'Acme Hosting') => ({ org: { id: 'o1', name, slug: 'acme', role: 'owner', created_at: '2026-01-01T00:00:00Z' } });

/** Waits until the router is back on General after a declined question. */
async function stayed() {
  expect(await screen.findByLabelText('Name')).toBeInTheDocument();
  expect(screen.queryByText('Members page')).not.toBeInTheDocument();
}

describe('OrgGeneralPage leave question', () => {
  it('asks once when a dirty card is left, and not after Discard', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme': org() });
    const { toMembers } = renderPage();
    await user.type(await screen.findByLabelText('Name'), ' X');

    await toMembers();
    // One question, though two cards share the page.
    expect(await screen.findAllByRole('dialog', { name: 'Are you sure?' })).toHaveLength(1);
    expect(await declineConfirm(user)).toBe(LEAVE);
    await stayed();

    await user.click(screen.getByRole('button', { name: 'Discard' }));
    await toMembers();
    expect(await screen.findByText('Members page')).toBeInTheDocument();
    expect(screen.queryByText(LEAVE)).not.toBeInTheDocument();
  });

  it('leaves after Leave is confirmed', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme': org() });
    const { toMembers } = renderPage();
    await user.type(await screen.findByLabelText('Name'), ' X');
    await toMembers();
    await acceptConfirm(user);
    expect(await screen.findByText('Members page')).toBeInTheDocument();
  });

  it('does not ask after a successful save', async () => {
    const user = userEvent.setup();
    let name = 'Acme Hosting';
    mockApi({
      'GET /api/v1/orgs/acme': () => org(name),
      'PATCH /api/v1/orgs/acme': (req: MockRequest) => {
        name = (req.body as { name: string }).name;
        return org(name);
      },
    });
    const { toMembers } = renderPage();
    await user.type(await screen.findByLabelText('Name'), ' X');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await screen.findByText('Organization saved');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled());

    await toMembers();
    expect(await screen.findByText('Members page')).toBeInTheDocument();
    expect(screen.queryByText(LEAVE)).not.toBeInTheDocument();
  });
});

describe('OrgNameCard save', () => {
  it('keeps Save disabled for an empty name or one that only differs in surrounding spaces (item 11)', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme': org() });
    renderPage();
    const input = await screen.findByLabelText('Name');
    const save = screen.getByRole('button', { name: 'Save' });

    await user.type(input, '  ');
    expect(screen.getByRole('button', { name: 'Discard' })).toBeInTheDocument();
    expect(save).toBeDisabled();

    await user.clear(input);
    await user.type(input, '   ');
    expect(save).toBeDisabled();

    await user.clear(input);
    await user.type(input, 'Acme Cloud');
    expect(save).toBeEnabled();
  });

  it('keeps an edit typed while the save was in flight (R6)', async () => {
    const user = userEvent.setup();
    let name = 'Acme Hosting';
    let answer!: () => void;
    mockApi({
      'GET /api/v1/orgs/acme': () => org(name),
      'PATCH /api/v1/orgs/acme': async (req: MockRequest) => {
        await new Promise<void>((resolve) => (answer = resolve));
        name = (req.body as { name: string }).name;
        return org(name);
      },
    });
    renderPage();
    const input = await screen.findByLabelText('Name');
    await user.clear(input);
    await user.type(input, 'Acme Cloud');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(answer).toBeDefined());
    await user.type(input, ' EU');
    answer();
    await screen.findByText('Organization saved');

    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Acme Cloud EU'));
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Discard' }));
    expect(screen.getByLabelText('Name')).toHaveValue('Acme Cloud');
  });
});
