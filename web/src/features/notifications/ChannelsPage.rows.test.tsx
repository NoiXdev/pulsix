import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { clippedElements } from '../../test/clipped.ts';
import { ChannelsPage } from './ChannelsPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const hook = {
  id: 'c1', kind: 'webhook', name: 'ops hook', enabled: true, revision: 2, created_at: '', updated_at: '',
  webhook: { method: 'POST', url: 'https://hooks.example.test/…', headers: [], signed: false },
};
const mailCh = { id: 'c2', kind: 'email', name: 'ops-mail', enabled: false, revision: 1, created_at: '', updated_at: '', email: { recipients: ['ops@example.test'] } };

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/settings/notifications', component: ChannelsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/notifications'] }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { ...view, router };
}

function serve(role: string, extra: Record<string, unknown> = {}, mailAvailable = true) {
  mockApi({
    'GET /api/v1/orgs/acme': org(role),
    'GET /api/v1/orgs/acme/notification-channels': { channels: [hook, mailCh], mail_available: mailAvailable },
    'GET /api/v1/orgs/acme/notification-digest': { digest: { enabled: false, weekday: 'mon', time: '08:00', timezone: 'UTC', channels: [] } },
    'GET /api/v1/orgs/acme/notification-channels/c2/deliveries': { deliveries: [] },
    ...extra,
  });
}

describe('ChannelsPage rows', () => {
  it("opens a channel's deliveries from its row menu", async () => {
    const user = userEvent.setup();
    serve('admin');
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for ops-mail' }));
    await user.click(screen.getByRole('menuitem', { name: 'Deliveries' }));
    expect(await screen.findByRole('dialog', { name: 'Deliveries · ops-mail' })).toBeInTheDocument();
  });

  it('sends a test, edits and deletes from the row menu', async () => {
    const user = userEvent.setup();
    let tested = 0;
    let deleted = 0;
    serve('admin', {
      'POST /api/v1/orgs/acme/notification-channels/c1/test': () => {
        tested++;
        return { delivery: { id: 'd', event_kind: 'test', status: 'sent', attempts: 1, created_at: '' } };
      },
      'DELETE /api/v1/orgs/acme/notification-channels/c1': () => {
        deleted++;
        return new Response(null, { status: 204 });
      },
    });
    const confirm = vi.spyOn(window, 'confirm');
    renderPage();

    const menu = async (item: string) => {
      await user.click(await screen.findByRole('button', { name: 'Actions for ops hook' }));
      await user.click(screen.getByRole('menuitem', { name: item }));
    };

    await menu('Send test');
    await waitFor(() => expect(tested).toBe(1));
    expect(await screen.findByText('Test message sent through ops hook')).toBeInTheDocument();

    await menu('Edit');
    expect(await screen.findByRole('dialog', { name: 'Edit ops hook' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    // Declining the confirmation deletes nothing; accepting it does.
    confirm.mockReturnValueOnce(false);
    await menu('Delete…');
    expect(confirm).toHaveBeenLastCalledWith(expect.stringContaining('Delete channel ops hook?'));
    expect(deleted).toBe(0);
    confirm.mockReturnValueOnce(true);
    await menu('Delete…');
    await waitFor(() => expect(deleted).toBe(1));
    confirm.mockRestore();
  });

  it('gives an operator only Deliveries', async () => {
    const user = userEvent.setup();
    serve('operator');
    renderPage();
    // Settle the role first: until the org has loaded, nothing is rendered
    // and the absence checks below could not fail.
    await screen.findByText('ops hook');
    expect(screen.queryByRole('button', { name: 'Add channel' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Actions for ops hook' }));
    const items = screen.getAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Deliveries']);
  });

  it('says when e-mail is not set up', async () => {
    serve('admin', {}, false);
    renderPage();
    expect(await screen.findByText('E-mail is not set up on this installation')).toBeInTheDocument();
    expect(screen.getByText(/Settings → Outgoing mail/)).toBeInTheDocument();
  });

  it('is silent about e-mail when it is set up', async () => {
    serve('admin');
    renderPage();
    await screen.findByText('ops hook');
    expect(screen.queryByText('E-mail is not set up on this installation')).not.toBeInTheDocument();
  });

  it('rows are not links', async () => {
    const user = userEvent.setup();
    serve('admin');
    const { container, router } = renderPage();
    await screen.findByText('ops-mail');
    const rows = container.querySelectorAll<HTMLElement>('[data-list-row]');
    expect(rows).toHaveLength(2);
    for (const row of rows) {
      expect(row.querySelector(':scope > a')).toBeNull();
      expect(within(row).queryAllByRole('link')).toEqual([]);
    }
    // Only the ⋯ trigger is a button in a row, and using it never navigates.
    const before = router.state.location.href;
    await user.click(screen.getByRole('button', { name: 'Actions for ops-mail' }));
    await user.click(screen.getByRole('menuitem', { name: 'Deliveries' }));
    expect(router.state.location.href).toBe(before);
  });

  it('no clipped cards', async () => {
    serve('admin', {}, false);
    const { container } = renderPage();
    await screen.findByText('E-mail is not set up on this installation');
    await screen.findByText('ops-mail');
    expect(clippedElements(container)).toEqual([]);
  });
});
