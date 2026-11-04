import { beforeEach, describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
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

function renderPage(routes: Record<string, MockRoute>) {
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
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
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

describe('ChangePasswordPage (a temporary password must be replaced)', () => {
  it('renders without the app shell, names the account and offers to sign out', async () => {
    renderPage({ 'GET /api/v1/auth/me': FORCED });

    expect(await screen.findByRole('heading', { name: 'Choose a new password' })).toBeInTheDocument();
    expect(screen.getByText(/sara@acme\.io/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Sign out/ })).toBeInTheDocument();
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
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
    [422, 'invalid input: the password must be at least 12 characters', /at least 12 characters/],
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
