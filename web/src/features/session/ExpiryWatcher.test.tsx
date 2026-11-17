import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { ExpiryWatcher } from './ExpiryWatcher.tsx';

const T0 = Date.parse('2026-11-17T10:00:00Z');
const SEC = 1000;

function iso(ms: number) {
  return new Date(ms).toISOString();
}

/** An /auth/me answer. The Date header is the (fake) local clock plus
 * `clockOffset`, or absent for null: a server whose clock differs by that much. */
function me(expiresAt: number, clockOffset: number | null = 0, extra: Record<string, unknown> = {}) {
  return () =>
    new Response(
      JSON.stringify({ locked: false, mfa_complete: true, session_expires_at: iso(expiresAt), user: { display_name: 'Tim' }, ...extra }),
      {
        status: 200,
        headers: {
          'Content-Type': 'application/json',
          ...(clockOffset === null ? {} : { Date: new Date(Date.now() + clockOffset).toUTCString() }),
        },
      },
    );
}

function calls(method: string, path: string) {
  return fetchMock.mock.calls.filter(([input]) => {
    const r = input as Request;
    return r.method === method && new URL(r.url, 'http://localhost').pathname === path;
  }).length;
}

let qc: QueryClient;

async function renderIt(routes: Record<string, MockRoute>) {
  mockApi(routes);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: ExpiryWatcher });
  const home = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <div>HOME</div> });
  const login = createRoute({ getParentRoute: () => rootRoute, path: '/login', component: () => <div>LOGIN</div> });
  const router = createRouter({ routeTree: rootRoute.addChildren([home, login]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  await router.load();
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  // /auth/me answers through real event-loop turns; wait for it without
  // moving the fake clock.
  for (let i = 0; i < 200 && !qc.getQueryData(['me']); i++) await new Promise((r) => setImmediate(r));
  await advance(0);
  return router;
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
    // Query notifications and effects chain through zero-delay timers.
    // The mocked fetch also needs real event-loop turns (stream reads), so
    // setImmediate stays real.
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setImmediate(r));
      await vi.advanceTimersByTimeAsync(0);
    }
  });
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] });
  vi.setSystemTime(T0);
});

afterEach(() => {
  vi.useRealTimers();
});

function webauthnStub() {
  const get = vi.fn().mockResolvedValue({ id: 'cred' });
  Object.defineProperty(navigator, 'credentials', { value: { get }, configurable: true });
  Object.defineProperty(window, 'PublicKeyCredential', { value: class {}, configurable: true });
  return get;
}

vi.mock('../auth/webauthnCodec.ts', async (orig) => ({
  ...(await orig<typeof import('../auth/webauthnCodec.ts')>()),
  decodeRequestOptions: (o: unknown) => o,
  encodeAssertionCredential: () => ({ id: 'cred' }),
}));

/** until polls an assertion across real event-loop turns without moving the
 * fake clock (RTL's waitFor polls on the faked timers and would hang). */
async function until(assertion: () => void) {
  let last: unknown;
  for (let i = 0; i < 100; i++) {
    try {
      assertion();
      return;
    } catch (e) {
      last = e;
    }
    await advance(0);
  }
  throw last;
}

// userEvent and RTL's async wrapper wait on setTimeout, which is faked here.
const user = () => ({
  type: (el: HTMLElement, value: string) => {
    fireEvent.change(el, { target: { value } });
    return Promise.resolve();
  },
  click: (el: HTMLElement) => {
    fireEvent.click(el);
    return Promise.resolve();
  },
});

describe('ExpiryWatcher', () => {
  it('shows the dialog at 5:00 left and not at 5:01', async () => {
    await renderIt({ 'GET /api/v1/auth/me': me(T0 + 301 * SEC) });
    await advance(0);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    await advance(999); // 5:00.001 left
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    await advance(1); // 5:00 left
    expect(screen.getByRole('dialog', { name: 'Your session is about to end' })).toBeInTheDocument();
    expect(screen.getByText('5:00')).toBeInTheDocument();

    await advance(61 * SEC);
    expect(screen.getByText('3:59')).toBeInTheDocument();
  });

  it('shows the dialog at once when the session is already inside the window', async () => {
    await renderIt({ 'GET /api/v1/auth/me': me(T0 + 90 * SEC) });
    await advance(0);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByText('1:30')).toBeInTheDocument();
  });

  it('counts down on the server clock when the local clock is skewed', async () => {
    // The browser is ten minutes ahead of the server. Against the local
    // clock the session would look long over; the Date header says 4:00 left.
    vi.setSystemTime(T0 + 600 * SEC);
    await renderIt({ 'GET /api/v1/auth/me': me(T0 + 240 * SEC, -600 * SEC) });
    await advance(0);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByText('4:00')).toBeInTheDocument();
    expect(screen.queryByText('LOGIN')).not.toBeInTheDocument();
  });

  it('falls back to the local clock without a Date header', async () => {
    await renderIt({ 'GET /api/v1/auth/me': me(T0 + 240 * SEC, null) });
    await advance(0);
    expect(screen.getByText('4:00')).toBeInTheDocument();
  });

  it('renews with the password and a code, closes, refetches me and tells the other tabs', async () => {
    const heard = vi.fn();
    const other = new BroadcastChannel('pulsix-session');
    other.onmessage = (e) => heard(e.data);
    let meCalls = 0;
    const renew = vi.fn(() => ({ session_expires_at: iso(T0 + 3600 * SEC) }));
    await renderIt({
      'GET /api/v1/auth/me': () => me(meCalls++ === 0 ? T0 + 240 * SEC : T0 + 3600 * SEC)(),
      'POST /api/v1/auth/session/renew': renew,
    });
    await advance(0);
    const u = user();
    await u.type(screen.getByLabelText('Password'), 'hunter2');
    await u.type(screen.getByLabelText('Authenticator or recovery code'), '123456');
    await u.click(screen.getByRole('button', { name: 'Renew session' }));
    await advance(0);

    await until(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(renew).toHaveBeenCalledWith(expect.objectContaining({ body: { password: 'hunter2', code: '123456' } }));
    // Once for the renewal, once more when this tab hears its own message.
    expect(calls('GET', '/api/v1/auth/me')).toBeGreaterThanOrEqual(2);
    await advance(10);
    await until(() => expect(heard).toHaveBeenCalledWith({ type: 'renewed', expiresAt: iso(T0 + 3600 * SEC) }));
    other.close();
  });

  it('sends a recovery code as recovery_code', async () => {
    const renew = vi.fn(() => ({ session_expires_at: iso(T0 + 3600 * SEC) }));
    await renderIt({ 'GET /api/v1/auth/me': me(T0 + 240 * SEC), 'POST /api/v1/auth/session/renew': renew });
    await advance(0);
    const u = user();
    await u.type(screen.getByLabelText('Password'), 'hunter2');
    await u.type(screen.getByLabelText('Authenticator or recovery code'), 'abcd-efgh');
    await u.click(screen.getByRole('button', { name: 'Renew session' }));
    await advance(0);
    expect(renew).toHaveBeenCalledWith(expect.objectContaining({ body: { password: 'hunter2', recovery_code: 'abcd-efgh' } }));
  });

  it('shows the error and keeps the dialog on a wrong password', async () => {
    await renderIt({
      'GET /api/v1/auth/me': me(T0 + 240 * SEC),
      'POST /api/v1/auth/session/renew': () => new Response(JSON.stringify({ status: 401, detail: 'invalid credentials' }), { status: 401 }),
    });
    await advance(0);
    const u = user();
    await u.type(screen.getByLabelText('Password'), 'nope');
    await u.type(screen.getByLabelText('Authenticator or recovery code'), '000000');
    await u.click(screen.getByRole('button', { name: 'Renew session' }));
    await advance(0);

    await until(() => expect(screen.getByRole('alert')).toHaveTextContent('The password or the code is wrong.'));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('renews with a passkey through the re-auth routes, never the login ones', async () => {
    webauthnStub();
    const begin = vi.fn(() => ({ ceremony_id: 'c1', options: { publicKey: { challenge: 'x' } } }));
    const finish = vi.fn(() => ({ session_expires_at: iso(T0 + 3600 * SEC) }));
    const login = vi.fn(() => ({}));
    await renderIt({
      'GET /api/v1/auth/me': me(T0 + 240 * SEC),
      'POST /api/v1/auth/passkeys/reauth/begin': begin,
      'POST /api/v1/auth/session/renew/passkey': finish,
      'POST /api/v1/auth/passkeys/login/begin': login,
      'POST /api/v1/auth/passkeys/login/finish': login,
    });
    await advance(0);
    await user().click(screen.getByRole('button', { name: 'Use a passkey' }));
    await advance(0);

    await until(() => expect(finish).toHaveBeenCalledWith(expect.objectContaining({ body: { ceremony_id: 'c1', response: { id: 'cred' } } })));
    expect(begin).toHaveBeenCalledTimes(1);
    expect(login).not.toHaveBeenCalled();
    await until(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('closes when another tab renews', async () => {
    let meCalls = 0;
    await renderIt({ 'GET /api/v1/auth/me': () => me(meCalls++ === 0 ? T0 + 240 * SEC : T0 + 3600 * SEC)() });
    await advance(0);
    expect(screen.getByRole('dialog')).toBeInTheDocument();

    const other = new BroadcastChannel('pulsix-session');
    other.postMessage({ type: 'renewed', expiresAt: iso(T0 + 3600 * SEC) });
    other.close();
    await advance(10);

    await until(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(calls('GET', '/api/v1/auth/me')).toBe(2);
  });

  it('survives hearing its own renewal', async () => {
    // Every postSessionMessage opens its own channel instance, and the
    // watcher's listener in the same tab is another one: it hears it.
    let meCalls = 0;
    await renderIt({
      'GET /api/v1/auth/me': () => me(meCalls++ === 0 ? T0 + 240 * SEC : T0 + 3600 * SEC)(),
      'POST /api/v1/auth/session/renew': { session_expires_at: iso(T0 + 3600 * SEC) },
    });
    await advance(0);
    const u = user();
    await u.type(screen.getByLabelText('Password'), 'hunter2');
    await u.type(screen.getByLabelText('Authenticator or recovery code'), '123456');
    await u.click(screen.getByRole('button', { name: 'Renew session' }));
    await advance(50);
    await until(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(screen.queryByText('LOGIN')).not.toBeInTheDocument();
  });

  it('clears the cache and goes to /login with the notice at zero', async () => {
    const router = await renderIt({ 'GET /api/v1/auth/me': me(T0 + 20 * SEC) });
    await advance(0);
    const clear = vi.spyOn(qc, 'clear');
    expect(screen.getByRole('dialog')).toBeInTheDocument();

    await advance(19 * SEC);
    expect(router.state.location.pathname).toBe('/');
    await advance(1 * SEC);

    await until(() => expect(router.state.location.pathname).toBe('/login'));
    expect(router.state.location.search).toMatchObject({ notice: 'session_expired' });
    expect(clear).toHaveBeenCalled();
  });

  it('re-reads /auth/me when the tab becomes visible again', async () => {
    await renderIt({ 'GET /api/v1/auth/me': me(T0 + 3600 * SEC) });
    await advance(0);
    expect(calls('GET', '/api/v1/auth/me')).toBe(1);
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'));
    });
    await advance(0);
    expect(calls('GET', '/api/v1/auth/me')).toBe(2);
  });
});
