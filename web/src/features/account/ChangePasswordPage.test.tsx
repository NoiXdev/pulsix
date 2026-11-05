import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider, onlineManager } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { useMe } from '../../api/hooks/useMe.ts';
import { ChangePasswordPage } from './ChangePasswordPage.tsx';

const USER = { id: 'u1', email: 'sara@acme.io', display_name: 'Sara K.' };
const FORCED = {
  user: USER,
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: false,
  must_change_password: true,
};
const TEMPORARY = 'Tmp-pass-9f3k2L-x7Q';
const CHOSEN = 'my-own-long-password';

function renderPage(routes: Record<string, MockRoute>, qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  mockApi(routes);
  const rootRoute = createRootRoute();
  const passwordRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account/password', component: ChangePasswordPage });
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => <div>ROOT</div> });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: () => <div>LOGIN</div>,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([passwordRoute, indexRoute, loginRoute]),
    history: createMemoryHistory({ initialEntries: ['/account/password'] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

async function fill(user: ReturnType<typeof userEvent.setup>, current: string, next: string, repeat = next) {
  await user.type(await screen.findByLabelText('Temporary password'), current);
  await user.type(screen.getByLabelText('New password'), next);
  await user.type(screen.getByLabelText('Repeat the new password'), repeat);
  await user.click(screen.getByRole('button', { name: 'Change password' }));
}

const refusal = (status: number, detail: string) =>
  new Response(JSON.stringify({ title: 'Refused', status, detail }), { status });

const changeCalls = () =>
  fetchMock.mock.calls.filter(([input]) => input instanceof Request && new URL(input.url).pathname === '/api/v1/auth/password');

beforeEach(() => {
  fetchMock.mockClear();
});

afterEach(() => {
  onlineManager.setOnline(true);
});

const offline = () => Promise.reject(new TypeError('Failed to fetch'));

/** A plain /auth/me reader on the default network mode, as the i18n
 * provider is for the whole session: offline, its fetch pauses. */
function MeReader() {
  const me = useMe();
  return <p>{`reader: ${me.fetchStatus}`}</p>;
}

describe('ChangePasswordPage (a temporary password must be replaced)', () => {
  it('renders without the app shell, names the account and offers to sign out', async () => {
    renderPage({ 'GET /api/v1/auth/me': FORCED });

    expect(await screen.findByRole('heading', { name: 'Choose a new password' })).toBeInTheDocument();
    expect(screen.getByText(/sara@acme\.io/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Sign out/ })).toBeInTheDocument();
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
  });

  // A cached /auth/me from before the change still says the account must
  // change its password; the page waits for the refetch on mount instead
  // of showing the form on it.
  it('decides on a fresh /auth/me, not on a stale cached one', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['me'], FORCED);
    let shown = false;
    const observer = new MutationObserver(() => {
      if (document.body.textContent?.includes('Choose a new password')) shown = true;
    });
    observer.observe(document.body, { childList: true, subtree: true, characterData: true });

    renderPage({ 'GET /api/v1/auth/me': { ...FORCED, must_change_password: false } }, qc);

    expect(await screen.findByText('ROOT')).toBeInTheDocument();
    observer.disconnect();
    expect(shown).toBe(false);
  });

  // Offline, /auth/me used to pause rather than fail, leaving the page on
  // "Loading…" for good; with a cached answer it then acted on that stale one.
  it.each([
    ['nothing cached', undefined],
    ['an answer cached from before', FORCED],
  ])('offline, shows the failure instead of waiting (%s)', async (_, cached) => {
    onlineManager.setOnline(false);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    if (cached) qc.setQueryData(['me'], cached);

    renderPage({ 'GET /api/v1/auth/me': offline }, qc);

    expect(await screen.findByRole('heading', { name: 'Something went wrong' })).toBeInTheDocument();
    expect(screen.getByText('Failed to fetch')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Choose a new password' })).not.toBeInTheDocument();
    expect(screen.queryByText(/Could not refresh/)).not.toBeInTheDocument();
  });

  // Another reader's fetch, paused offline, must not hold the page: the
  // page's own fetch would otherwise only resume that paused one.
  it('offline, shows the failure behind another reader\'s paused fetch', async () => {
    onlineManager.setOnline(false);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mockApi({ 'GET /api/v1/auth/me': offline });
    render(
      <QueryClientProvider client={qc}>
        <MeReader />
      </QueryClientProvider>,
    );
    expect(await screen.findByText('reader: paused')).toBeInTheDocument();

    renderPage({ 'GET /api/v1/auth/me': offline }, qc);

    expect(await screen.findByRole('heading', { name: 'Something went wrong' })).toBeInTheDocument();
    expect(screen.getByText('Failed to fetch')).toBeInTheDocument();
  });

  // Once a fresh answer is on screen, a later refetch that fails (the one on
  // reconnect while the network is still flaky, or another reader
  // invalidating /auth/me) must not take the form away.
  it('keeps the form through a failed background refetch', async () => {
    let down = false;
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderPage({ 'GET /api/v1/auth/me': () => (down ? offline() : FORCED) }, qc);
    expect(await screen.findByRole('heading', { name: 'Choose a new password' })).toBeInTheDocument();

    down = true;
    await act(() => qc.refetchQueries({ queryKey: ['me'] }));

    expect(await screen.findByText(/Could not refresh/)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Choose a new password' })).toBeInTheDocument();
  });

  it('signs out to /login and clears the cache', async () => {
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderPage({ 'GET /api/v1/auth/me': FORCED, 'POST /api/v1/auth/logout': {} }, qc);

    await user.click(await screen.findByRole('button', { name: /Sign out/ }));

    expect(await screen.findByText('LOGIN')).toBeInTheDocument();
    await waitFor(() => expect(qc.getQueryCache().getAll()).toHaveLength(0));
  });

  it('posts the temporary and the new password, then continues into the app', async () => {
    const user = userEvent.setup();
    let changed = false;
    const posted: unknown[] = [];
    renderPage({
      'GET /api/v1/auth/me': () => ({ ...FORCED, must_change_password: !changed }),
      'POST /api/v1/auth/password': (req: { body: unknown }) => {
        posted.push(req.body);
        changed = true;
        return new Response(null, { status: 204 });
      },
    });

    await fill(user, TEMPORARY, CHOSEN);

    expect(await screen.findByText('ROOT')).toBeInTheDocument();
    expect(posted).toEqual([{ current_password: TEMPORARY, new_password: CHOSEN }]);
  });

  it('refuses a new password equal to the temporary one on the client', async () => {
    const user = userEvent.setup();
    renderPage({ 'GET /api/v1/auth/me': FORCED });

    await fill(user, 'same-password-twice', 'same-password-twice');

    expect(await screen.findByText(/must differ from the temporary one/)).toBeInTheDocument();
    expect(changeCalls()).toEqual([]);
  });

  it('refuses a short or mistyped new password on the client', async () => {
    const user = userEvent.setup();
    renderPage({ 'GET /api/v1/auth/me': FORCED });

    await fill(user, TEMPORARY, 'too-short');
    expect(await screen.findByText('at least 12 characters')).toBeInTheDocument();

    await user.clear(screen.getByLabelText('New password'));
    await user.clear(screen.getByLabelText('Repeat the new password'));
    await user.type(screen.getByLabelText('New password'), CHOSEN);
    await user.type(screen.getByLabelText('Repeat the new password'), `${CHOSEN}x`);
    await user.click(screen.getByRole('button', { name: 'Change password' }));
    expect(await screen.findByText('passwords do not match')).toBeInTheDocument();
    expect(changeCalls()).toEqual([]);
  });

  it.each([
    [400, 'invalid_current_password', /The temporary password is not correct/],
    [400, 'temporary_password_expired', /Ask an instance administrator for a new one/],
    [422, 'same_password', /must differ from the temporary one/],
    [422, 'password_too_short', /^at least 12 characters$/],
  ])('explains a %i %s refusal in words', async (status, detail, want) => {
    const user = userEvent.setup();
    renderPage({
      'GET /api/v1/auth/me': FORCED,
      'POST /api/v1/auth/password': () => refusal(status, detail),
    });

    await fill(user, TEMPORARY, CHOSEN);

    expect(await screen.findByText(want)).toBeInTheDocument();
    expect(screen.queryByText('ROOT')).not.toBeInTheDocument();
  });

  it('asks for the second factor again when the proof is stale, then changes the password', async () => {
    const user = userEvent.setup();
    let stale = true;
    let changed = false;
    renderPage({
      'GET /api/v1/auth/me': () => ({ ...FORCED, must_change_password: !changed }),
      'POST /api/v1/auth/totp/verify': () => {
        stale = false;
        return {};
      },
      'POST /api/v1/auth/password': () => {
        if (stale) return refusal(403, 'step_up_required');
        changed = true;
        return new Response(null, { status: 204 });
      },
    });

    await fill(user, TEMPORARY, CHOSEN);
    expect(await screen.findByText('Confirm it is you')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Authenticator or recovery code'), '123456');
    await user.click(screen.getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('ROOT')).toBeInTheDocument();
    await waitFor(() => expect(changeCalls()).toHaveLength(2));
  });

  it.each([
    ['has nothing to change', { ...FORCED, must_change_password: false }],
    ['has not passed its second factor', { ...FORCED, mfa_complete: false, mfa_enrolment_required: true }],
  ])('sends a session that %s on to the root redirect, which knows where it belongs', async (_, me) => {
    renderPage({ 'GET /api/v1/auth/me': me });

    expect(await screen.findByText('ROOT')).toBeInTheDocument();
  });
});
