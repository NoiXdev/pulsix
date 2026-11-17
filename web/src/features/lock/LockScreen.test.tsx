import { describe, expect, it, vi } from 'vitest';
import { useState } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Outlet, createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { LockScreen } from './LockScreen.tsx';
import { LockStateContext } from './lockState.ts';

const me = { locked: false, lock_pin_set: true, user: { display_name: 'Tim' } };

function Harness() {
  const [locked, setLocked] = useState(true);
  return (
    <LockStateContext.Provider value={{ locked, setLocked }}>
      {locked ? <LockScreen /> : <><div>UNLOCKED PAGE</div><Outlet /></>}
    </LockStateContext.Provider>
  );
}

function renderIt(routes: Record<string, MockRoute>) {
  mockApi({ 'GET /api/v1/auth/me': me, ...routes });
  const rootRoute = createRootRoute({ component: Harness });
  const login = createRoute({ getParentRoute: () => rootRoute, path: '/login', component: () => <div>LOGIN</div> });
  const router = createRouter({ routeTree: rootRoute.addChildren([login]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const wrong = (left: number) => () =>
  new Response(JSON.stringify({ title: 'Unprocessable', status: 422, detail: 'wrong pin', attempts_left: left }), { status: 422 });

describe('LockScreen', () => {
  it('offers a numeric PIN field, Unlock and Sign out', async () => {
    renderIt({});
    const pin = await screen.findByLabelText('PIN');
    expect(pin).toHaveAttribute('inputmode', 'numeric');
    expect(pin).toHaveAttribute('autocomplete', 'off');
    expect(screen.getByRole('button', { name: 'Unlock' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument();
  });

  it('shows the attempts left after a wrong PIN', async () => {
    renderIt({ 'POST /api/v1/auth/lock/unlock': wrong(4) });
    await userEvent.type(await screen.findByLabelText('PIN'), '0000');
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText(/4 attempts left/)).toBeInTheDocument();
    expect(screen.queryByText('UNLOCKED PAGE')).not.toBeInTheDocument();
  });

  it('goes away when the PIN is right', async () => {
    const unlock = vi.fn(() => ({ ok: true }));
    renderIt({ 'POST /api/v1/auth/lock/unlock': unlock });
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText('UNLOCKED PAGE')).toBeInTheDocument();
    expect(unlock).toHaveBeenCalledTimes(1);
  });

  it('tells the other tabs once unlocked', async () => {
    const heard = vi.fn();
    const other = new BroadcastChannel('pulsix-session');
    other.onmessage = (e) => heard(e.data);
    renderIt({ 'POST /api/v1/auth/lock/unlock': { ok: true } });
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    await vi.waitFor(() => expect(heard).toHaveBeenCalledWith({ type: 'unlocked' }));
    other.close();
  });

  it('goes to /login with the notice when the fifth wrong PIN ends the session', async () => {
    const router = renderIt({
      'POST /api/v1/auth/lock/unlock': () => new Response(JSON.stringify({ detail: 'session ended' }), { status: 401 }),
    });
    await userEvent.type(await screen.findByLabelText('PIN'), '0000');
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/login');
    expect(router.state.location.search).toMatchObject({ notice: 'lock_ended' });
  });

  it('signs out to /login', async () => {
    const logout = vi.fn(() => ({ ok: true }));
    const router = renderIt({ 'POST /api/v1/auth/logout': logout });
    await userEvent.click(await screen.findByRole('button', { name: 'Sign out' }));
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
    expect(logout).toHaveBeenCalled();
    expect(router.state.location.pathname).toBe('/login');
  });
});
