import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { BootstrapDialog } from './BootstrapDialog.tsx';

function renderDialog(onClose = () => undefined) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/o/$org',
    component: () => (
      <>
        <BootstrapDialog org="acme" hostId="h1" hostName="web-04.fra" onClose={onClose} />
        <Outlet />
      </>
    ),
  });
  const runRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([runRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('BootstrapDialog', () => {
  it('submits a one-time password login and lands on the run view', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'POST /api/v1/orgs/acme/hosts/h1/bootstrap': (req: MockRequest) => {
        posted = req.body;
        return { job_id: 'job-1' };
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Bootstrap web-04.fra' });
    await user.type(within(dialog).getByLabelText('Login user'), 'ansible');
    await user.type(within(dialog).getByLabelText('One-time password'), 'hunter2');
    expect(within(dialog).getByText(/used once and never stored/i)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Bootstrap host' }));
    expect(posted).toEqual({ user: 'ansible', password: 'hunter2' });
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
  });

  it('sends a private key instead of a password when that mode is chosen', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'POST /api/v1/orgs/acme/hosts/h1/bootstrap': (req: MockRequest) => {
        posted = req.body;
        return { job_id: 'job-1' };
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Bootstrap web-04.fra' });
    await user.type(within(dialog).getByLabelText('Login user'), 'ansible');
    await user.click(within(dialog).getByRole('radio', { name: /private key/i }));
    await user.type(within(dialog).getByLabelText('One-time private key'), '-----BEGIN OPENSSH PRIVATE KEY-----');
    await user.click(within(dialog).getByRole('button', { name: 'Bootstrap host' }));
    expect(posted).toEqual({ user: 'ansible', private_key: '-----BEGIN OPENSSH PRIVATE KEY-----' });
  });

  it('refuses to submit without exactly one credential', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'POST /api/v1/orgs/acme/hosts/h1/bootstrap': (req: MockRequest) => {
        posted.push(req.body);
        return { job_id: 'job-1' };
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Bootstrap web-04.fra' });
    await user.type(within(dialog).getByLabelText('Login user'), 'ansible');
    await user.click(within(dialog).getByRole('button', { name: 'Bootstrap host' }));
    expect(await within(dialog).findByText(/exactly one of password and private key/i)).toBeInTheDocument();
    expect(posted).toEqual([]);

    // Switching to the key mode submits the key, and the payload still
    // carries exactly one credential.
    await user.click(within(dialog).getByRole('radio', { name: /private key/i }));
    await user.type(within(dialog).getByLabelText('One-time private key'), '-----BEGIN OPENSSH PRIVATE KEY-----');
    await user.click(within(dialog).getByRole('button', { name: 'Bootstrap host' }));
    expect(posted).toEqual([{ user: 'ansible', private_key: '-----BEGIN OPENSSH PRIVATE KEY-----' }]);
  });
});
