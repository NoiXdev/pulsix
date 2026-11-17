import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { mockApi } from '../../test/mockApi.ts';
import { ExpiryWatcher } from './ExpiryWatcher.tsx';
import { ExpiryNotice, ExpiryNoticeFallback, ExpiryNoticeProvider } from './ExpiryNotice.tsx';

/** A session that ends in four minutes: inside the warning. */
function meEnding() {
  return { locked: false, mfa_complete: true, session_expires_at: new Date(Date.now() + 240_000).toISOString(), user: { display_name: 'Tim' } };
}

/** A stand-in for AppShell: a header with a control, then the notice slot. */
function Shell() {
  return (
    <div>
      <header data-testid="header">
        <button type="button">Lock session</button>
      </header>
      <div data-testid="slot">
        <ExpiryNotice />
      </div>
      <main>PAGE</main>
    </div>
  );
}

function renderIt(withShell: boolean) {
  mockApi({ 'GET /api/v1/auth/me': meEnding() });
  const rootRoute = createRootRoute({
    component: () => (
      <ExpiryNoticeProvider>
        <div data-testid="fallback">
          <ExpiryNoticeFallback />
        </div>
        {withShell ? <Shell /> : <main>BARE PAGE</main>}
        <ExpiryWatcher />
      </ExpiryNoticeProvider>
    ),
  });
  const login = createRoute({ getParentRoute: () => rootRoute, path: '/login', component: () => <div>LOGIN</div> });
  const router = createRouter({ routeTree: rootRoute.addChildren([login]), history: createMemoryHistory({ initialEntries: ['/'] }) });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

describe('ExpiryNotice', () => {
  it('draws the collapsed warning in the shell slot, in the flow, with the countdown and Renew', async () => {
    renderIt(true);
    await userEvent.click(await screen.findByRole('button', { name: 'Later' }));

    const banner = await within(screen.getByTestId('slot')).findByRole('status');
    expect(banner).toHaveTextContent(/Your session ends in [34]:\d\d/);
    expect(banner.className).not.toMatch(/\bfixed\b|\babsolute\b|\bz-/);
    expect(within(banner).getByRole('button', { name: 'Renew' })).toBeInTheDocument();
    expect(within(screen.getByTestId('fallback')).queryByRole('status')).not.toBeInTheDocument();
    expect(screen.getAllByRole('status')).toHaveLength(1);

    await userEvent.click(within(banner).getByRole('button', { name: 'Renew' }));
    expect(await screen.findByRole('dialog', { name: 'Your session is about to end' })).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('draws it above a page without a shell', async () => {
    renderIt(false);
    await userEvent.click(await screen.findByRole('button', { name: 'Later' }));

    const banner = await within(screen.getByTestId('fallback')).findByRole('status');
    expect(banner).toHaveTextContent(/Your session ends in [34]:\d\d/);
    expect(screen.getAllByRole('status')).toHaveLength(1);
  });
});
