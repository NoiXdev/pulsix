import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { ResetPasswordPage } from './ResetPasswordPage.tsx';

const TOKEN = 'abc-reset-token-1234';
const PASSWORD = 'a-new-long-password';

/** Opens the page the way the reset mail does: the token rides in the
 * fragment of the address the browser shows. */
function renderPage(routes: Record<string, MockRoute>, hash = `#token=${TOKEN}`) {
  window.history.replaceState(null, '', `/reset-password${hash}`);
  mockApi(routes);
  const rootRoute = createRootRoute();
  const resetRoute = createRoute({ getParentRoute: () => rootRoute, path: '/reset-password', component: ResetPasswordPage });
  const forgotRoute = createRoute({ getParentRoute: () => rootRoute, path: '/forgot-password', component: () => <div>FORGOT</div> });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: () => <div>LOGIN</div>,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([resetRoute, forgotRoute, loginRoute]),
    history: createMemoryHistory({ initialEntries: ['/reset-password'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

async function submit(user: ReturnType<typeof userEvent.setup>, next: string, repeat = next) {
  await user.type(await screen.findByLabelText('New password'), next);
  await user.type(screen.getByLabelText('Repeat the new password'), repeat);
  await user.click(screen.getByRole('button', { name: 'Set password' }));
}

const requestedUrls = () => fetchMock.mock.calls.map(([input]) => (input instanceof Request ? input.url : String(input)));

beforeEach(() => {
  fetchMock.mockClear();
});

afterEach(() => {
  window.history.replaceState(null, '', '/');
});

describe('ResetPasswordPage', () => {
  it('reads the token from the fragment and sends it in the body only, never in a request URL', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    renderPage({
      'POST /api/v1/auth/password/reset': (req: { body: unknown }) => {
        posted.push(req.body);
        return new Response(null, { status: 204 });
      },
    });

    await submit(user, PASSWORD);

    await waitFor(() => expect(posted).toEqual([{ token: TOKEN, new_password: PASSWORD }]));
    expect(requestedUrls().length).toBeGreaterThan(0);
    for (const url of requestedUrls()) expect(url).not.toContain(TOKEN);
  });

  it('takes the token out of the address bar once it has read it', async () => {
    renderPage({});

    await screen.findByLabelText('New password');
    expect(window.location.hash).toBe('');
    expect(window.location.href).not.toContain(TOKEN);
  });

  it('refuses a password under 12 characters on the client', async () => {
    const user = userEvent.setup();
    renderPage({});

    await submit(user, 'short-pass1');

    expect(await screen.findByText('at least 12 characters')).toBeInTheDocument();
    expect(requestedUrls()).toEqual([]);
  });

  it('refuses two passwords that differ on the client', async () => {
    const user = userEvent.setup();
    renderPage({});

    await submit(user, PASSWORD, `${PASSWORD}x`);

    expect(await screen.findByText('passwords do not match')).toBeInTheDocument();
    expect(requestedUrls()).toEqual([]);
  });

  it('says the link is invalid or expired on a 400 and offers a new one', async () => {
    const user = userEvent.setup();
    renderPage({
      'POST /api/v1/auth/password/reset': () =>
        new Response(JSON.stringify({ title: 'Bad Request', status: 400, detail: 'invalid_or_expired_link' }), { status: 400 }),
    });

    await submit(user, PASSWORD);

    expect(await screen.findByText(/This link is invalid or has expired/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Request a new link' })).toHaveAttribute('href', '/forgot-password');
  });

  it('says the same, without a form, when the address carries no token', async () => {
    renderPage({}, '');

    expect(await screen.findByText(/This link is invalid or has expired/)).toBeInTheDocument();
    expect(screen.queryByLabelText('New password')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Request a new link' })).toHaveAttribute('href', '/forgot-password');
  });

  it('sends the person to sign in with the new password and their second factor on success', async () => {
    const user = userEvent.setup();
    renderPage({ 'POST /api/v1/auth/password/reset': () => new Response(null, { status: 204 }) });

    await submit(user, PASSWORD);

    expect(await screen.findByText(/Sign in with your new password and your second factor/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Go to sign in' })).toHaveAttribute('href', '/login');
    expect(screen.queryByLabelText('New password')).not.toBeInTheDocument();
  });

  // Only the server's own code means the length rule (Task 12): any other
  // 422 says something else, and the page shows that instead.
  it.each([
    ['password_too_short', 'at least 12 characters'],
    ['the password is on a list of breached passwords', 'the password is on a list of breached passwords'],
  ])('reads a 422 %s for what it says', async (detail, want) => {
    const user = userEvent.setup();
    renderPage({
      'POST /api/v1/auth/password/reset': () =>
        new Response(JSON.stringify({ title: 'Unprocessable Entity', status: 422, detail }), { status: 422 }),
    });

    await submit(user, PASSWORD);

    expect(await screen.findByText(want)).toBeInTheDocument();
  });
});
