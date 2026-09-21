import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ScheduleNotifications } from './ScheduleNotifications.tsx';

const channels = {
  channels: [
    { id: 'c1', kind: 'webhook', name: 'ops hook', enabled: true, revision: 1, created_at: '', updated_at: '' },
    { id: 'c2', kind: 'email', name: 'ops mail', enabled: true, revision: 1, created_at: '', updated_at: '' },
  ],
  mail_available: true,
};
const rules = {
  rules: [
    { on: 'failure', channels: [{ id: 'c1', name: 'ops hook', kind: 'webhook' }, { id: 'c2', name: 'ops mail', kind: 'email' }] },
    { on: 'change', channels: [{ id: 'c1', name: 'ops hook', kind: 'webhook' }] },
  ],
};

/** QueryError — the guard element a failed rules query renders — calls
 * useNavigate (for the 401 redirect), so the section renders inside a
 * minimal router like the ScheduleCalendar tests do. */
function renderIt(canWrite: boolean, qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({
    getParentRoute: () => orgRoute, path: '/schedules', component: () => <ScheduleNotifications org="acme" scheduleId="s1" canWrite={canWrite} />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/schedules'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('ScheduleNotifications', () => {
  it('shows the rules as text to readers', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/schedules/s1/notifications': rules });
    renderIt(false);
    expect(await screen.findByText('→ ops hook, ops mail')).toBeInTheDocument();
    expect(screen.getByText('on change')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save notifications' })).not.toBeInTheDocument();
  });

  it('edits the rules as a channel × category matrix', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/schedules/s1/notifications': rules,
      'GET /api/v1/orgs/acme/notification-channels': channels,
      'PUT /api/v1/orgs/acme/schedules/s1/notifications': (req: MockRequest) => {
        put = req.body;
        return rules;
      },
    });
    renderIt(true);
    const hookChange = await screen.findByRole('checkbox', { name: 'ops hook on change' });
    expect(hookChange).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'ops mail on failure' })).toBeChecked();
    await user.click(screen.getByRole('checkbox', { name: 'ops mail on failure' }));
    await user.click(screen.getByRole('checkbox', { name: 'ops mail skipped / missed' }));
    await user.click(screen.getByRole('button', { name: 'Save notifications' }));
    await waitFor(() =>
      expect(put).toEqual({
        rules: [
          { on: 'failure', channels: ['c1'] },
          { on: 'change', channels: ['c1'] },
          { on: 'skipped', channels: ['c2'] },
        ],
      }),
    );
    expect(await screen.findByText('Notifications saved')).toBeInTheDocument();
  });

  it('says so when the channels cannot be loaded', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/schedules/s1/notifications': rules,
      'GET /api/v1/orgs/acme/notification-channels': () => new Response('{"detail":"nope"}', { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderIt(true);
    expect(await screen.findByRole('alert')).toHaveTextContent('The channels could not be loaded');
    // The rules themselves still read, so the section is not empty.
    expect(screen.getByText('→ ops hook, ops mail')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save notifications' })).not.toBeInTheDocument();
  });

  it('points to the settings when the organization has no channel', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/schedules/s1/notifications': { rules: [] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [], mail_available: true },
    });
    renderIt(true);
    expect(await screen.findByText(/admins add them under Settings → Notifications/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save notifications' })).toBeDisabled();
  });
});

/**
 * Backlog 2.85: a failed rules query used to `return null` — the whole
 * Notifications section vanished instead of saying why. The channel-list
 * failure keeps its bespoke surface (the reader-view alert above); only
 * the rules query itself now goes through useQueryGuard, like every
 * other guarded page.
 */
describe('a failed rules query (backlog 2.85)', () => {
  it('reports the failure instead of vanishing', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/schedules/s1/notifications': () =>
        new Response(JSON.stringify({ detail: 'internal error' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderIt(false);
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByText('internal error')).toBeInTheDocument();
    expect(screen.queryByText(/No notifications/)).not.toBeInTheDocument();
  });

  it('renders the rules again once a refetch succeeds', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let failing = true;
    mockApi({
      'GET /api/v1/orgs/acme/schedules/s1/notifications': () =>
        failing
          ? new Response(JSON.stringify({ detail: 'internal error' }), { status: 500, headers: { 'Content-Type': 'application/json' } })
          : rules,
    });
    renderIt(false, qc);
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    failing = false;
    await qc.refetchQueries();
    expect(await screen.findByText('→ ops hook, ops mail')).toBeInTheDocument();
    expect(screen.queryByText('Something went wrong')).not.toBeInTheDocument();
  });
});
