import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { OrgGeneralPage } from './OrgGeneralPage.tsx';

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const generalRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/settings/general',
    component: OrgGeneralPage,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([generalRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/general'] }),
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
              <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
          </QueryClientProvider>,
  );
}

const org = (role: string, name = 'Acme Hosting') => ({
  org: { id: 'o1', name, slug: 'acme', role, created_at: '2026-01-01T00:00:00Z' },
});

describe('OrgGeneralPage', () => {
  it('saves a changed name through PATCH and shows the new name', async () => {
    const user = userEvent.setup();
    let name = 'Acme Hosting';
    let patched: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': () => org('owner', name),
      'PATCH /api/v1/orgs/acme': (req: MockRequest) => {
        patched = req.body;
        name = (req.body as { name: string }).name;
        return org('owner', name);
      },
    });
    renderPage();
    const input = await screen.findByLabelText('Organization name');
    expect(input).toHaveValue('Acme Hosting');
    await user.clear(input);
    await user.type(input, 'Acme Cloud');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(patched).toEqual({ name: 'Acme Cloud' }));
    await waitFor(() => expect(screen.getByLabelText('Organization name')).toHaveValue('Acme Cloud'));
  });

  // The save resolves only once the refetched organization is in the cache:
  // clearing the draft earlier would show the old name until the refetch
  // lands.
  it('never shows the old name again between a save and the refetched name', async () => {
    const user = userEvent.setup();
    let name = 'Acme Hosting';
    mockApi({
      'GET /api/v1/orgs/acme': async () => {
        const answer = org('owner', name);
        if (name !== 'Acme Hosting') await new Promise((r) => setTimeout(r, 150));
        return answer;
      },
      'PATCH /api/v1/orgs/acme': (req: MockRequest) => {
        name = (req.body as { name: string }).name;
        return org('owner', name);
      },
    });
    renderPage();
    const input = await screen.findByLabelText('Organization name');
    await user.clear(input);
    await user.type(input, 'Acme Cloud');

    const seen: string[] = [];
    const observer = new MutationObserver(() => {
      const field = screen.queryByLabelText<HTMLInputElement>('Organization name');
      if (field) seen.push(field.value);
    });
    observer.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true });
    await user.click(screen.getByRole('button', { name: 'Save' }));
    // The toast is raised right after the save resolves.
    await screen.findByText('Organization saved');
    expect((screen.getByLabelText('Organization name') as HTMLInputElement).value).toBe('Acme Cloud');
    await waitFor(() => expect(screen.getByLabelText('Organization name')).toHaveValue('Acme Cloud'));
    observer.disconnect();
    expect(seen).not.toContain('Acme Hosting');
  });

  it('shows the slug read-only', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org('owner') });
    renderPage();
    const slug = await screen.findByLabelText('Slug');
    expect(slug).toHaveValue('acme');
    expect(slug).toHaveAttribute('readonly');
  });

  it('shows the name read-only, with no Save button, to a role without org.manage', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org('operator') });
    renderPage();
    const input = await screen.findByLabelText('Organization name');
    expect(input).toHaveAttribute('readonly');
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
  });
});
