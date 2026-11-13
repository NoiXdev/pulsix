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
  // Backlog 3.57: the organization's monitoring setting, beside its name.
  // The absent warning for client hosts fires after this many days without
  // contact; 0 switches it off; the server takes 0..365.
  describe('monitoring', () => {
    const LABEL = 'Warn after N days without contact (0 = off)';
    function routes(role: string, days = 14, onPut?: (req: MockRequest) => void) {
      let current = days;
      return {
        'GET /api/v1/orgs/acme': org(role),
        'GET /api/v1/orgs/acme/settings/monitoring': () => ({ settings: { client_absent_days: current } }),
        'PUT /api/v1/orgs/acme/settings/monitoring': (req: MockRequest) => {
          onPut?.(req);
          current = (req.body as { client_absent_days: number }).client_absent_days;
          return { settings: { client_absent_days: current } };
        },
      };
    }

    it('shows the stored value and saves a new one', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', 14, (req) => puts.push(req.body)));
      renderPage();

      const input = await screen.findByLabelText(LABEL);
      await waitFor(() => expect(input).toHaveValue(14));
      await user.clear(input);
      await user.type(input, '0');
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));

      await waitFor(() => expect(puts).toEqual([{ client_absent_days: 0 }]));
      expect(await screen.findByText('Monitoring settings saved')).toBeInTheDocument();
    });

    it.each([['366'], ['-1'], ['1.5'], ['']])('refuses %j without asking the server', async (typed) => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('admin', 14, (req) => puts.push(req.body)));
      renderPage();

      const input = await screen.findByLabelText(LABEL);
      await waitFor(() => expect(input).toHaveValue(14));
      await user.clear(input);
      if (typed !== '') await user.type(input, typed);
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));

      expect(await screen.findByText('Enter a whole number of days from 0 to 365.')).toBeInTheDocument();
      expect(puts).toEqual([]);
    });

    it('accepts both ends of the range', async () => {
      const user = userEvent.setup();
      const puts: unknown[] = [];
      mockApi(routes('owner', 14, (req) => puts.push(req.body)));
      renderPage();

      const input = await screen.findByLabelText(LABEL);
      await waitFor(() => expect(input).toHaveValue(14));
      await user.clear(input);
      await user.type(input, '365');
      await user.click(screen.getByRole('button', { name: 'Save monitoring settings' }));
      await waitFor(() => expect(puts).toEqual([{ client_absent_days: 365 }]));
    });

    it('is read-only below admin', async () => {
      mockApi(routes('operator', 7));
      renderPage();

      const input = await screen.findByLabelText(LABEL);
      await waitFor(() => expect(input).toHaveValue(7));
      expect(input).toHaveAttribute('readonly');
      expect(screen.queryByRole('button', { name: 'Save monitoring settings' })).not.toBeInTheDocument();
      expect(screen.getByText('Only owners and admins can change the monitoring settings.')).toBeInTheDocument();
    });
  });
});
