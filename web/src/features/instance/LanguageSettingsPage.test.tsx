import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { LanguageSettingsPage } from './LanguageSettingsPage.tsx';

const ADMIN = {
  user: { id: 'u1', email: 'admin@acme.io', display_name: 'Ada' },
  mfa_complete: true,
  totp_confirmed: true,
  instance_admin: true,
  locale: null,
};

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const languageRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/settings/language',
    component: LanguageSettingsPage,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([languageRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/language'] }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <I18nProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe('LanguageSettingsPage', () => {
  it('saves the instance default, and a user without a language of their own follows it', async () => {
    const user = userEvent.setup();
    let instanceDefault: 'en' | 'de' = 'en';
    let saved: unknown;
    mockApi({
      'GET /api/v1/auth/me': () => ({ ...ADMIN, effective_locale: instanceDefault }),
      'GET /api/v1/setup/status': () => ({ completed: true, version: 'dev', default_locale: instanceDefault }),
      'GET /api/v1/admin/settings/locale': () => ({ default_locale: instanceDefault }),
      'GET /api/v1/orgs/acme': { org: { id: 'o1', slug: 'acme', name: 'Acme', role: 'owner' } },
      'PUT /api/v1/admin/settings/locale': (req: MockRequest) => {
        saved = req.body;
        instanceDefault = (req.body as { default_locale: 'en' | 'de' }).default_locale;
        return new Response(null, { status: 204 });
      },
    });
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Default language' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText('Default language')).toHaveValue('English'));

    await pickOption(screen, 'Default language', 'Deutsch');
    await user.click(screen.getByRole('button', { name: 'Save default language' }));

    // The 204 carries nothing: the admin's own effective language comes
    // back from the refetched /auth/me, and the page is German.
    expect(await screen.findByRole('heading', { name: 'Standardsprache' })).toBeInTheDocument();
    expect(saved).toEqual({ default_locale: 'de' });
    const toast = await screen.findByRole('status');
    expect(within(toast).getByText('Standardsprache gespeichert')).toBeInTheDocument();
  });

  it('shows the API error when saving fails', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, effective_locale: 'en' },
      'GET /api/v1/setup/status': { completed: true, version: 'dev', default_locale: 'en' },
      'GET /api/v1/admin/settings/locale': { default_locale: 'en' },
      'GET /api/v1/orgs/acme': { org: { id: 'o1', slug: 'acme', name: 'Acme', role: 'owner' } },
      'PUT /api/v1/admin/settings/locale': () =>
        new Response(JSON.stringify({ detail: 'instance administrators only' }), {
          status: 403,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();

    await waitFor(() => expect(screen.getByLabelText('Default language')).toHaveValue('English'));
    await user.click(screen.getByRole('button', { name: 'Save default language' }));
    const toast = await screen.findByRole('status');
    expect(within(toast).getByText('instance administrators only')).toBeInTheDocument();
  });
});
