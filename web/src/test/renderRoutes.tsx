import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createRootRoute,
  createRoute,
  createRouter,
  createMemoryHistory,
  RouterProvider,
  type AnyRoute,
} from '@tanstack/react-router';
import { ToastProvider } from '../ui/toast.tsx';
import { I18nProvider } from '../i18n/I18nProvider.tsx';
import type { Locale } from '../i18n/types.ts';

/** One page of a test: a path under /o/$org and what it renders. */
export interface TestRoute {
  path: string;
  component: () => React.ReactNode;
}

/**
 * Renders the given pages under /o/$org in a memory router with a query
 * client and toasts, starting at `initial`. Returns the router, so a test
 * can read where navigation ended up. A test that inspects the cache passes
 * its own query client; one that reads German passes `locale`.
 */
export function renderRoutes(
  routes: TestRoute[],
  initial: string,
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
  locale?: Locale,
) {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const children: AnyRoute[] = routes.map((r) =>
    createRoute({ getParentRoute: () => orgRoute, path: r.path, component: r.component }),
  );
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren(children)]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
  const page = (
    <ToastProvider>
      <RouterProvider router={router} />
    </ToastProvider>
  );
  render(<QueryClientProvider client={qc}>{locale ? <I18nProvider locale={locale}>{page}</I18nProvider> : page}</QueryClientProvider>);
  return router;
}

export const ADMIN = {
  user: { id: 'u1', email: 'admin@acme.io', display_name: 'Ada' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: true,
};
