import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi, type MockRoute } from '../../test/mockApi.ts';
import { ForgotPasswordPage } from './ForgotPasswordPage.tsx';

function renderPage(routes: Record<string, MockRoute>) {
  mockApi(routes);
  const rootRoute = createRootRoute();
  const forgotRoute = createRoute({ getParentRoute: () => rootRoute, path: '/forgot-password', component: ForgotPasswordPage });
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    validateSearch: (s: Record<string, unknown>) => ({ step: typeof s.step === 'string' ? s.step : undefined }),
    component: () => <div>LOGIN</div>,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([forgotRoute, loginRoute]),
    history: createMemoryHistory({ initialEntries: ['/forgot-password'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

const SENT = 'If an account exists for this address, we sent a link';

describe('ForgotPasswordPage', () => {
  it('sends the person to an instance administrator, with no form, when no mail is configured', async () => {
    renderPage({ 'GET /api/v1/auth/password/reset-available': { available: false } });

    expect(await screen.findByText(/ask an instance administrator/)).toBeInTheDocument();
    expect(screen.queryByLabelText('Email address')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Send link' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to sign in' })).toHaveAttribute('href', '/login');
  });

  it('posts the address and answers neutrally, saying nothing about whether an account exists', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    renderPage({
      'GET /api/v1/auth/password/reset-available': { available: true },
      'POST /api/v1/auth/password/forgot': (req: { body: unknown }) => {
        posted.push(req.body);
        return new Response('{}', { status: 202, headers: { 'Content-Type': 'application/json' } });
      },
    });

    await user.type(await screen.findByLabelText('Email address'), 'nobody@acme.io');
    await user.click(screen.getByRole('button', { name: 'Send link' }));

    expect(await screen.findByText(new RegExp(SENT))).toBeInTheDocument();
    expect(posted).toEqual([{ email: 'nobody@acme.io' }]);
    expect(screen.queryByLabelText('Email address')).not.toBeInTheDocument();
  });

  it('asks the person to wait when they are rate limited', async () => {
    const user = userEvent.setup();
    renderPage({
      'GET /api/v1/auth/password/reset-available': { available: true },
      'POST /api/v1/auth/password/forgot': () =>
        new Response(JSON.stringify({ title: 'Too Many Requests', status: 429 }), {
          status: 429,
          headers: { 'Retry-After': '60' },
        }),
    });

    await user.type(await screen.findByLabelText('Email address'), 'ada@acme.io');
    await user.click(screen.getByRole('button', { name: 'Send link' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/too many attempts/);
    expect(screen.queryByText(new RegExp(SENT))).not.toBeInTheDocument();
  });

  it('says the request could not be sent, and keeps the form, when the server fails', async () => {
    const user = userEvent.setup();
    renderPage({
      'GET /api/v1/auth/password/reset-available': { available: true },
      'POST /api/v1/auth/password/forgot': () =>
        new Response(JSON.stringify({ title: 'Internal Server Error', status: 500 }), { status: 500 }),
    });

    await user.type(await screen.findByLabelText('Email address'), 'ada@acme.io');
    await user.click(screen.getByRole('button', { name: 'Send link' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('The request could not be sent. Try again.');
    expect(screen.queryByText(new RegExp(SENT))).not.toBeInTheDocument();
    expect(screen.getByLabelText('Email address')).toHaveValue('ada@acme.io');
  });

  it('says so when it cannot tell whether a link can be sent', async () => {
    renderPage({
      'GET /api/v1/auth/password/reset-available': () =>
        new Response(JSON.stringify({ detail: 'internal error' }), { status: 500 }),
    });

    expect(await screen.findByText(/Could not check whether a reset link can be sent/)).toBeInTheDocument();
    expect(screen.queryByLabelText('Email address')).not.toBeInTheDocument();
  });
});
