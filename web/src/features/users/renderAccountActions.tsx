import type { QueryClient } from '@tanstack/react-query';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import type { AdminUserDetail } from '../../api/types.ts';
import { AccountActions } from './AccountActions.tsx';

/** Renders AccountActions for `user` on its account page route, with mail known to be available. */
export function renderAccountActions(
  user: AdminUserDetail,
  routes: Record<string, MockRoute> = {},
  selfId = 'u1',
  qc?: QueryClient,
) {
  mockApi({ 'GET /api/v1/auth/password/reset-available': { available: true }, ...routes });
  return renderRoutes(
    [
      { path: '/settings/users/$userId', component: () => <AccountActions user={user} selfId={selfId} /> },
      { path: '/settings/users', component: () => <div>USERS</div> },
    ],
    `/o/acme/settings/users/${user.id}`,
    qc,
  );
}

export const ACTIONS = [
  'Send password reset link',
  'Set temporary password',
  'Reset second factor',
  'End all sessions',
  'Deactivate account',
];
