import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi } from '../../test/mockApi.ts';
import { LockPinPromptPage } from './LockPinPromptPage.tsx';

const ME = {
  user: { id: 'u1', email: 'tim@acme.io', display_name: 'Tim R.' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: false,
  must_change_password: false,
  lock_pin_set: false,
  lock_pin_prompt: true,
};

function setup() {
  const put = vi.fn(() => ({ ok: true }));
  const skipped = vi.fn(() => ({ ok: true }));
  mockApi({
    'GET /api/v1/auth/me': ME,
    'PUT /api/v1/auth/lock/pin': put,
    'POST /api/v1/auth/lock/prompt-skipped': skipped,
  });
  const rootRoute = createRootRoute();
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <div>ROOT</div> });
  const pageRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/lock-pin', component: LockPinPromptPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, pageRoute]),
    history: createMemoryHistory({ initialEntries: ['/account/lock-pin'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { put, skipped };
}

describe('LockPinPromptPage', () => {
  it('Skip records the skip, then continues to /', async () => {
    const { put, skipped } = setup();
    await userEvent.click(await screen.findByRole('button', { name: 'Skip' }));
    expect(await screen.findByText('ROOT')).toBeInTheDocument();
    expect(skipped).toHaveBeenCalledTimes(1);
    expect(put).not.toHaveBeenCalled();
  });

  it('Set PIN puts the PIN, then continues to /', async () => {
    const { put, skipped } = setup();
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    await userEvent.type(screen.getByLabelText('Repeat the PIN'), '1234');
    await userEvent.click(screen.getByRole('button', { name: 'Set PIN' }));
    expect(await screen.findByText('ROOT')).toBeInTheDocument();
    expect(put).toHaveBeenCalledTimes(1);
    expect(skipped).not.toHaveBeenCalled();
  });

  it('refuses a mismatch without calling the server or leaving', async () => {
    const { put } = setup();
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    await userEvent.type(screen.getByLabelText('Repeat the PIN'), '1235');
    await userEvent.click(screen.getByRole('button', { name: 'Set PIN' }));
    expect(await screen.findByText('The two PINs do not match.')).toBeInTheDocument();
    await waitFor(() => expect(put).not.toHaveBeenCalled());
    expect(screen.queryByText('ROOT')).not.toBeInTheDocument();
  });
});
