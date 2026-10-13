import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { RotationsPage } from './RotationsPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const me = { user: { id: 'u1', email: 'a@acme.io', display_name: 'A' }, mfa_complete: true, mfa_enrolment_required: false, totp_confirmed: true, instance_admin: false };
const channel = { id: 'c1', kind: 'email', name: 'Ops mail', enabled: true, revision: 1, created_at: '', updated_at: '', email: { recipients: ['ops@example.test'] } };

const rotation = {
  id: 'rot1',
  name: 'Weekly',
  period_days: 7,
  anchor: '2026-01-05T09:00:00+01:00',
  timezone: 'Europe/Berlin',
  channels: ['c1'],
  revision: 1,
  created_at: '',
  updated_at: '',
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/settings/oncall', component: RotationsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/oncall'] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('RotationsPage', () => {
  it('shows a rotation and its roster', async () => {
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/oncall-rotations': { rotations: [rotation] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/oncall-rotations/rot1/shifts': {
        shifts: [{ starts_at: '2026-01-05T09:00:00Z', ends_at: '2026-01-12T09:00:00Z', channel_id: 'c1', channel_name: 'Ops mail', stand_in: false }],
      },
      'GET /api/v1/orgs/acme/oncall-rotations/rot1/exceptions': { exceptions: [] },
    });
    renderPage();

    expect(await screen.findByText('Weekly')).toBeInTheDocument();
    expect(await screen.findAllByText('Ops mail')).not.toHaveLength(0);
  });

  // The roster is the only place the page says who is on call, and the one
  // thing that goes wrong with it in practice — a rotation whose last
  // channel was deleted, or whose timezone no longer loads — arrives as a
  // failed request, not as an empty shift list: Shifts always tiles the
  // range it is given, so RosterStrip's own "nobody is on call" branch is
  // unreachable from here. Without an error branch the section printed
  // "Loading roster…" forever, which is the one thing that is not true
  // (whole-branch review, Important #4).
  it('says the roster could not be built rather than loading forever', async () => {
    mockApi({
      'GET /api/v1/auth/me': me,
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/oncall-rotations': { rotations: [rotation] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [channel], mail_available: true },
      'GET /api/v1/orgs/acme/oncall-rotations/rot1/shifts': () =>
        new Response(JSON.stringify({ detail: 'rotation has no channels' }), { status: 400, headers: { 'Content-Type': 'application/json' } }),
      'GET /api/v1/orgs/acme/oncall-rotations/rot1/exceptions': { exceptions: [] },
    });
    renderPage();

    expect(await screen.findByText(/roster could not be built/i)).toBeInTheDocument();
    expect(screen.queryByText(/loading roster/i)).not.toBeInTheDocument();
  });
});
