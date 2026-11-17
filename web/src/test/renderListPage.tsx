import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider, type AnyRoute } from '@tanstack/react-router';
import { ConfirmProvider } from '../ui/confirm.tsx';
import { ToastProvider } from '../ui/toast.tsx';
import { I18nProvider } from '../i18n/I18nProvider.tsx';
import type { Locale } from '../i18n/types.ts';
import { validateListSearch } from '../features/lists/listSearch.ts';

export interface ListPageOptions {
  /** The list route's path under /o/$org, e.g. '/alerts'. */
  path: string;
  component: () => React.ReactNode;
  /** The URL the test starts at, e.g. '/o/acme/alerts?page=2'. */
  initial: string;
  /** Search keys of the page besides sort/order/page (the filters in the URL). */
  extraSearch?: (search: Record<string, unknown>) => Record<string, unknown>;
  /** Other pages the list links to. */
  others?: Array<{ path: string; component: () => React.ReactNode }>;
  locale?: Locale;
}

/**
 * Renders a server-paged list page the way router.tsx mounts it: under
 * /o/$org, its search validated with validateListSearch (plus the page's
 * own keys). Returns the router for assertions on the URL.
 */
export function renderListPage(opts: ListPageOptions) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: opts.path,
    validateSearch: (s: Record<string, unknown>) => ({ ...validateListSearch(s), ...opts.extraSearch?.(s) }),
    component: opts.component,
  });
  const others: AnyRoute[] = (opts.others ?? []).map((o) => createRoute({ getParentRoute: () => orgRoute, path: o.path, component: o.component }));
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, ...others])]),
    history: createMemoryHistory({ initialEntries: [opts.initial] }),
  });
  const page = (
    <ToastProvider>
      <ConfirmProvider>
        <RouterProvider router={router} />
      </ConfirmProvider>
    </ToastProvider>
  );
  render(<QueryClientProvider client={qc}>{opts.locale ? <I18nProvider locale={opts.locale}>{page}</I18nProvider> : page}</QueryClientProvider>);
  return router;
}
