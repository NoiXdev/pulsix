import { describe, expect, it, vi } from 'vitest';
import { useState } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Outlet, createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { LockScreen } from './LockScreen.tsx';
import { LockStateContext } from './lockState.ts';
import { meQueryKey } from '../../api/hooks/useMe.ts';

const me = { locked: false, lock_pin_set: true, lock_pin_legacy: false, user: { display_name: 'Tim' } };

function Harness() {
  const [locked, setLocked] = useState(true);
  return (
    <LockStateContext.Provider value={{ locked, setLocked }}>
      {locked ? <LockScreen /> : <><div>UNLOCKED PAGE</div><Outlet /></>}
    </LockStateContext.Provider>
  );
}

let qcRef: QueryClient;

function renderIt(routes: Record<string, MockRoute>, cachedMe?: Record<string, unknown>) {
  mockApi({ 'GET /api/v1/auth/me': me, ...routes });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  if (cachedMe) qc.setQueryData(meQueryKey, cachedMe);
  const rootRoute = createRootRoute({ component: Harness });
  const login = createRoute({ getParentRoute: () => rootRoute, path: '/login', component: () => <div>LOGIN</div> });
  const router = createRouter({ routeTree: rootRoute.addChildren([login]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  qcRef = qc;
  return router;
}

const wrong = (left: number) => () =>
  new Response(JSON.stringify({ title: 'Unprocessable', status: 422, detail: 'wrong pin', attempts_left: left }), { status: 422 });

describe('LockScreen', () => {
  it('offers four PIN boxes and Sign out, without an Unlock button', async () => {
    renderIt({}, me);
    const pin = await screen.findByLabelText('PIN');
    expect(pin).toHaveAttribute('inputmode', 'numeric');
    expect(pin).toHaveAttribute('autocomplete', 'off');
    expect(pin).toHaveAttribute('maxlength', '4');
    expect(document.querySelectorAll('[data-pin-box]')).toHaveLength(4);
    expect(screen.queryByRole('button', { name: 'Unlock' })).not.toBeInTheDocument();
    expect(screen.queryByText(/older version/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument();
  });

  it('keeps the single field, Unlock and a note for a legacy PIN', async () => {
    const unlock = vi.fn(() => ({ ok: true }));
    renderIt({ 'POST /api/v1/auth/lock/unlock': unlock }, { ...me, lock_pin_legacy: true });
    const pin = await screen.findByLabelText('PIN');
    expect(pin).toHaveAttribute('maxlength', '8');
    expect(document.querySelectorAll('[data-pin-box]')).toHaveLength(0);
    expect(screen.getByText(/older version and longer than 4 digits/)).toBeInTheDocument();
    await userEvent.type(pin, '12345678');
    expect(unlock).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText('UNLOCKED PAGE')).toBeInTheDocument();
    expect(unlock).toHaveBeenCalledTimes(1);
  });

  it('falls back to the single field and Unlock, without the note, when /auth/me is not cached', async () => {
    renderIt({});
    const pin = await screen.findByLabelText('PIN');
    expect(pin).toHaveAttribute('maxlength', '8');
    expect(document.querySelectorAll('[data-pin-box]')).toHaveLength(0);
    expect(screen.getByRole('button', { name: 'Unlock' })).toBeInTheDocument();
    expect(screen.queryByText(/older version/)).not.toBeInTheDocument();
  });

  it('does not post a partial PIN on Enter', async () => {
    const unlock = vi.fn(() => ({ ok: true }));
    renderIt({ 'POST /api/v1/auth/lock/unlock': unlock }, me);
    await userEvent.type(await screen.findByLabelText('PIN'), '12{Enter}');
    expect(unlock).not.toHaveBeenCalled();
  });

  it('clears the boxes, shows the attempts left and refocuses after a wrong PIN', async () => {
    renderIt({ 'POST /api/v1/auth/lock/unlock': wrong(3) }, me);
    const pin = await screen.findByLabelText('PIN');
    await userEvent.type(pin, '0000');
    expect(await screen.findByText(/Wrong PIN\. 3 attempts left/)).toBeInTheDocument();
    await vi.waitFor(() => expect(pin).toHaveFocus());
    expect(pin).toHaveValue('');
  });

  it('shows the attempts left after a wrong PIN', async () => {
    renderIt({ 'POST /api/v1/auth/lock/unlock': wrong(4) }, me);
    await userEvent.type(await screen.findByLabelText('PIN'), '0000');
    expect(await screen.findByText(/4 attempts left/)).toBeInTheDocument();
    expect(screen.queryByText('UNLOCKED PAGE')).not.toBeInTheDocument();
  });

  it('keeps no wrong PIN in the mutation cache', async () => {
    renderIt({ 'POST /api/v1/auth/lock/unlock': wrong(4) }, me);
    await userEvent.type(await screen.findByLabelText('PIN'), '0815');
    expect(await screen.findByText(/4 attempts left/)).toBeInTheDocument();
    await vi.waitFor(() =>
      expect(JSON.stringify(qcRef.getMutationCache().getAll().map((m) => m.state.variables))).not.toContain('0815'),
    );
  });

  it('goes away when the PIN is right', async () => {
    const unlock = vi.fn(() => ({ ok: true }));
    renderIt({ 'POST /api/v1/auth/lock/unlock': unlock }, me);
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    expect(await screen.findByText('UNLOCKED PAGE')).toBeInTheDocument();
    expect(unlock).toHaveBeenCalledTimes(1);
  });

  it('tells the other tabs once unlocked', async () => {
    const heard = vi.fn();
    const other = new BroadcastChannel('pulsix-session');
    other.onmessage = (e) => heard(e.data);
    renderIt({ 'POST /api/v1/auth/lock/unlock': { ok: true } }, me);
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    await vi.waitFor(() => expect(heard).toHaveBeenCalledWith({ type: 'unlocked' }));
    other.close();
  });

  it('treats a 409 not-locked answer as unlocked', async () => {
    renderIt({
      'POST /api/v1/auth/lock/unlock': () => new Response(JSON.stringify({ detail: 'not locked' }), { status: 409 }),
    }, me);
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    expect(await screen.findByText('UNLOCKED PAGE')).toBeInTheDocument();
  });

  it('goes to /login with the notice when the fifth wrong PIN ends the session', async () => {
    const router = renderIt({
      'POST /api/v1/auth/lock/unlock': () =>
        new Response(JSON.stringify({ title: 'Unauthorized', status: 401, detail: 'too many wrong pins' }), { status: 401 }),
    }, me);
    await userEvent.type(await screen.findByLabelText('PIN'), '0000');
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/login');
    expect(router.state.location.search).toMatchObject({ notice: 'lock_ended' });
  });

  const gone = () =>
    new Response(JSON.stringify({ title: 'Unauthorized', status: 401, detail: 'login required' }), { status: 401 });

  it('goes to /login without the wrong-PIN notice when the session was gone for another reason', async () => {
    const router = renderIt(
      { 'POST /api/v1/auth/lock/unlock': gone },
      { ...me, session_expires_at: new Date(Date.now() + 3_600_000).toISOString() },
    );
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
    expect(router.state.location.pathname).toBe('/login');
    expect(router.state.location.search).not.toHaveProperty('notice');
  });

  it('goes to /login with the expiry notice when the session reached its end while locked', async () => {
    const router = renderIt(
      { 'POST /api/v1/auth/lock/unlock': gone },
      { ...me, session_expires_at: new Date(Date.now() - 60_000).toISOString() },
    );
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
    expect(router.state.location.search).toMatchObject({ notice: 'session_expired' });
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
