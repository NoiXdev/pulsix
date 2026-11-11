import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { CredentialsPage } from './CredentialsPage.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';
import { settled } from '../../test/settled.ts';

const cred = (over: Record<string, unknown>) => ({
  id: 'c1', name: 'acme-deploy', kind: 'ssh_key', username: 'ansible', public_key: 'ssh-ed25519 AAAA pulsix:acme:acme-deploy',
  fingerprint: 'SHA256:abc', key_type: 'ed25519', expired: false, expires_soon: false, used_by: { bastions: 1, hosts: 41 },
  created_at: '2026-06-01T00:00:00Z', updated_at: '2026-06-01T00:00:00Z', ...over,
});
const ME = { id: 'u', email: 'a@b', display_name: 'A', instance_admin: false };
const SNIPPETS = { authorized_keys_command: "echo 'ssh-ed25519 AAAA pulsix:acme:acme-deploy' >> ~/.ssh/authorized_keys", cloud_init: '#cloud-config', terraform: 'locals {}' };

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/credentials', component: CredentialsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/credentials'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

describe('CredentialsPage', () => {
  it('lists credentials with kind, usage and expiry', async () => {
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme/credentials': {
        credentials: [cred({}), cred({ id: 'c2', name: 'gitlab-ops', kind: 'git_token', public_key: undefined, key_type: undefined, expires_soon: true, expires_at: '2099-01-01T00:00:00Z', used_by: { bastions: 0, hosts: 0 } })],
      },
    });
    renderPage();
    expect(await screen.findByText('SSH key · ed25519')).toBeInTheDocument();
    expect(screen.getByText(/41 hosts · 1 jump host/)).toBeInTheDocument();
    expect(screen.getByText(/expires in/)).toBeInTheDocument();
  });

  it('generates a key and shows its public key and snippets once', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'POST /api/v1/orgs/acme/credentials': (req: MockRequest) => {
        posted = req.body;
        return { credential: cred({ used_by: { bastions: 0, hosts: 0 } }), snippets: SNIPPETS };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add credential' }));
    await user.type(screen.getByLabelText('Name'), 'acme-deploy');
    await user.type(screen.getByLabelText('Username'), 'ansible');
    await user.click(screen.getByRole('button', { name: 'Create credential' }));
    expect(posted).toEqual({ name: 'acme-deploy', kind: 'ssh_key', username: 'ansible', generate: true });
    const panel = await screen.findByRole('dialog', { name: 'Install the public key' });
    expect(within(panel).getByLabelText('Public key')).toHaveTextContent('pulsix:acme:acme-deploy');
    expect(within(panel).getByLabelText('authorized_keys')).toHaveTextContent('authorized_keys');
    await user.click(within(panel).getByRole('tab', { name: 'cloud-init' }));
    expect(within(panel).getByLabelText('cloud-init')).toHaveTextContent('#cloud-config');
    await user.click(within(panel).getByRole('button', { name: 'Done' }));
  });

  it('creates a password credential and sends the password only once', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme/credentials': { credentials: [] },
      'POST /api/v1/orgs/acme/credentials': (req: MockRequest) => {
        posted = req.body;
        return { credential: cred({ kind: 'ssh_password', public_key: undefined, key_type: undefined, name: 'fs-admin' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add credential' }));
    await pickOption(screen, 'Kind', { value: 'ssh_password' });
    await user.type(screen.getByLabelText('Name'), 'fs-admin');
    await user.type(screen.getByLabelText('Password'), 'hunter2');
    await user.click(screen.getByRole('button', { name: 'Create credential' }));
    expect(posted).toEqual({ name: 'fs-admin', kind: 'ssh_password', password: 'hunter2' });
    expect(await screen.findByText('Credential fs-admin created')).toBeInTheDocument();
    expect(screen.queryByDisplayValue('hunter2')).not.toBeInTheDocument();
  });

  it('renames a credential and reports a refused delete', async () => {
    const user = userEvent.setup();
    let patched: unknown;
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme/credentials': { credentials: [cred({})] },
      'GET /api/v1/orgs/acme/credentials/c1': { credential: cred({}), snippets: SNIPPETS },
      'PATCH /api/v1/orgs/acme/credentials/c1': (req: MockRequest) => {
        patched = req.body;
        return { credential: cred({ name: 'acme-deploy-2' }), snippets: SNIPPETS };
      },
      'DELETE /api/v1/orgs/acme/credentials/c1': () =>
        new Response(JSON.stringify({ status: 409, detail: 'the credential is still used by a jump host or a host' }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'acme-deploy' }));
    const dialog = await screen.findByRole('dialog', { name: 'acme-deploy' });
    const name = within(dialog).getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'acme-deploy-2');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Credential saved')).toBeInTheDocument();
    expect(patched).toEqual({ name: 'acme-deploy-2', username: 'ansible' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await acceptConfirm(user);
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('still used');
  });

  it('asks before generating a new key and sends nothing when cancelled', async () => {    const user = userEvent.setup();
    const patches: unknown[] = [];
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme/credentials': { credentials: [cred({})] },
      'GET /api/v1/orgs/acme/credentials/c1': { credential: cred({}), snippets: SNIPPETS },
      'PATCH /api/v1/orgs/acme/credentials/c1': (req: MockRequest) => {
        patches.push(req.body);
        return { credential: cred({ fingerprint: 'SHA256:new' }), snippets: SNIPPETS };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'acme-deploy' }));
    const dialog = await screen.findByRole('dialog', { name: 'acme-deploy' });
    await user.click(within(dialog).getByRole('button', { name: 'Generate new key' }));
    expect(await declineConfirm(user)).toMatch(/replaces the key of acme-deploy/);
    await settled();
    expect(patches).toEqual([]);

    await user.click(within(dialog).getByRole('button', { name: 'Generate new key' }));
    await acceptConfirm(user);
    expect(await screen.findByText(/New key generated/)).toBeInTheDocument();
    expect(patches).toEqual([{ name: 'acme-deploy', username: 'ansible', generate: true }]);
  });

  it('shows a failed credential load inside the dialog instead of staying on Loading', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme/credentials': { credentials: [cred({})] },
      'GET /api/v1/orgs/acme/credentials/c1': () =>
        new Response(JSON.stringify({ status: 404, detail: 'credential not found' }), { status: 404, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'acme-deploy' }));
    const dialog = await screen.findByRole('dialog', { name: 'Credential' });
    expect(await within(dialog).findByText(/not found/i)).toBeInTheDocument();
    expect(within(dialog).queryByText('Loading…')).not.toBeInTheDocument();
  });

  it('shows a 500 credential load failure with its detail message inside the dialog', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme/credentials': { credentials: [cred({})] },
      'GET /api/v1/orgs/acme/credentials/c1': () =>
        new Response(JSON.stringify({ status: 500, detail: 'the vault is unreachable' }), { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'acme-deploy' }));
    const dialog = await screen.findByRole('dialog', { name: 'Credential' });
    expect(await within(dialog).findByText(/the vault is unreachable/)).toBeInTheDocument();
    expect(within(dialog).queryByText('Loading…')).not.toBeInTheDocument();
  });

  it('rotates an ssh_key credential after confirmation and shows the new fingerprint', async () => {
    const user = userEvent.setup();
    let rotated = false;
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'admin', created_at: '' } },
      'GET /api/v1/orgs/acme/credentials': () => ({ credentials: [cred({ fingerprint: rotated ? 'SHA256:new' : 'SHA256:old' })] }),
      'GET /api/v1/orgs/acme/credentials/c1': () => ({ credential: cred({ fingerprint: rotated ? 'SHA256:new' : 'SHA256:old' }), snippets: SNIPPETS }),
      'POST /api/v1/orgs/acme/credentials/c1/rotate': () => {
        rotated = true;
        return { job_id: '', job_count: 0 };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'acme-deploy' }));
    const dialog = await screen.findByRole('dialog', { name: 'acme-deploy' });

    // Declined: no request.
    await user.click(within(dialog).getByRole('button', { name: 'Rotate key' }));
    expect(await declineConfirm(user)).toMatch(/rotated|rotation/i);
    await settled();
    expect(rotated).toBe(false);

    // Confirmed: the rotation runs and the dialog refetches the credential.
    await user.click(within(dialog).getByRole('button', { name: 'Rotate key' }));
    await acceptConfirm(user);
    expect(await within(dialog).findByText(/SHA256:new/)).toBeInTheDocument();
  });

  it('gives an operator no rotate button (rotation is an admin write)', async () => {
    mockApi({
      'GET /api/v1/auth/me': ME,
      'GET /api/v1/orgs/acme': { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } },
      'GET /api/v1/orgs/acme/credentials': { credentials: [cred({})] },
      'GET /api/v1/orgs/acme/credentials/c1': { credential: cred({}), snippets: SNIPPETS },
    });
    renderPage();
    await userEvent.setup().click(await screen.findByRole('button', { name: 'acme-deploy' }));
    const dialog = await screen.findByRole('dialog', { name: 'acme-deploy' });
    expect(within(dialog).queryByRole('button', { name: 'Rotate key' })).not.toBeInTheDocument();
  });
});
