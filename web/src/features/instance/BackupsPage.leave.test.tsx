import { describe, expect, it } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ADMIN } from '../../test/renderRoutes.tsx';
import { declineConfirm } from '../../test/confirmDialog.ts';
import { BackupsPage } from './BackupsPage.tsx';

const LEAVE = 'Leave this page? Your unsaved changes will be lost.';

const SETTINGS = {
  destination: 'folder',
  s3: { endpoint: '', region: '', bucket: '', prefix: '', access_key: '', secret_key_set: false, path_style: false },
  enabled: true,
  time_of_day: '03:00',
  time_zone: 'Europe/Berlin',
  retention: { daily: 7, weekly: 4, monthly: 6 },
  include_metrics: true,
};

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const backupsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/backups', component: BackupsPage });
  const membersRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/members', component: () => <p>Members page</p> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([backupsRoute, membersRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/backups'] }),
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
  return () => act(() => void router.navigate({ to: '/o/$org/settings/members', params: { org: 'acme' } }));
}

function routes() {
  let settings = SETTINGS;
  return mockApi({
    'GET /api/v1/auth/me': ADMIN,
    'GET /api/v1/admin/backups/settings': () => settings,
    'GET /api/v1/admin/backups': { items: [], next_run_at: null, destination_label: 'Folder', storage_error: null },
    'PUT /api/v1/admin/backups/settings': (req: MockRequest) => {
      const body = req.body as typeof SETTINGS;
      settings = { ...settings, time_of_day: body.time_of_day };
      return new Response(null, { status: 204 });
    },
  });
}

describe('BackupsPage leave question', () => {
  it('asks once while a card is dirty, and not after Discard', async () => {
    const user = userEvent.setup();
    routes();
    const toMembers = renderPage();
    const time = await screen.findByLabelText('Time of day');
    await user.clear(time);
    await user.type(time, '05:30');

    await toMembers();
    expect(await screen.findAllByRole('dialog', { name: 'Are you sure?' })).toHaveLength(1);
    expect(await declineConfirm(user)).toBe(LEAVE);
    expect(screen.getByLabelText('Time of day')).toHaveValue('05:30');

    await user.click(screen.getByRole('button', { name: 'Discard' }));
    await toMembers();
    expect(await screen.findByText('Members page')).toBeInTheDocument();
    expect(screen.queryByText(LEAVE)).not.toBeInTheDocument();
  });

  it('does not ask after a successful save', async () => {
    const user = userEvent.setup();
    routes();
    const toMembers = renderPage();
    const time = await screen.findByLabelText('Time of day');
    await user.clear(time);
    await user.type(time, '05:30');
    await user.click(screen.getByRole('button', { name: 'Save schedule' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save schedule' })).toBeDisabled());

    await toMembers();
    expect(await screen.findByText('Members page')).toBeInTheDocument();
    expect(screen.queryByText(LEAVE)).not.toBeInTheDocument();
  });
});
