import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { HostSshCard } from './HostSshCard.tsx';

const KEY = { id: 'c1', name: 'inv-deploy', kind: 'ssh_key', username: 'ansible', public_key: 'ssh-ed25519 AAAA pulsix:acme:inv-deploy', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };

function renderCard(managed: boolean, canWrite = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <HostSshCard org="acme" hostId="h1" managed={managed} canWrite={canWrite} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const base = {
  'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { host_id: 'h1', username: '', port: 22, bastion_mode: 'inherit' } },
  'GET /api/v1/orgs/acme/bastions': {
    bastions: [
      { id: 'b1', name: 'bastion-a', address: 'a.example', port: 22, username: 'ops' },
      { id: 'b2', name: 'bastion-b', address: 'b.example', port: 2222, username: 'ops', via_bastion_id: 'b1' },
    ],
  },
  'GET /api/v1/orgs/acme/credentials': { credentials: [KEY] },
  'GET /api/v1/orgs/acme/credentials/c1': { credential: KEY, snippets: { authorized_keys_command: 'echo', cloud_init: '#cloud-config', terraform: 'locals {}' } },
  'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': {
    vars: { ansible_host: 'web-01' },
    connection: { host_id: 'h1', bastion_source: 'group:production', bastions: ['b1', 'b2'] },
    warnings: [],
  },
};

describe('HostSshCard', () => {
  it('renders nothing for a host that is not managed', () => {
    mockApi(base);
    renderCard(false);
    expect(screen.queryByText('SSH access')).not.toBeInTheDocument();
  });

  it('shows the resolved route, saves an explicit jump host, and offers the key to install', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      ...base,
      'PUT /api/v1/orgs/acme/hosts/h1/ssh': (req: MockRequest) => {
        put = req.body;
        return { ssh: { host_id: 'h1', username: '', port: 22, credential_id: 'c1', bastion_mode: 'explicit', bastion_id: 'b1' } };
      },
    });
    renderCard(true);
    expect(await screen.findByLabelText('Effective route')).toHaveTextContent('Route: inherited from group production · bastion-a → bastion-b → host');

    await user.selectOptions(screen.getByLabelText('Credential'), 'c1');
    expect(await screen.findByText('Install this key on the host')).toBeInTheDocument();
    expect(screen.getByLabelText('Public key')).toHaveTextContent('pulsix:acme:inv-deploy');

    await user.click(screen.getByLabelText(/This jump host/));
    await user.selectOptions(screen.getByLabelText('Jump host for this host'), 'b1');
    await user.click(screen.getByRole('button', { name: 'Save SSH settings' }));
    expect(await screen.findByText('SSH settings saved')).toBeInTheDocument();
    expect(put).toEqual({ port: 22, credential_id: 'c1', bastion_mode: 'explicit', bastion_id: 'b1' });
  });

  it('is read-only for a viewer: no save button, every field disabled', async () => {
    mockApi(base);
    renderCard(true, false);
    expect(await screen.findByLabelText('Effective route')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save SSH settings' })).not.toBeInTheDocument();
    expect(screen.getByLabelText('User')).toBeDisabled();
    expect(screen.getByLabelText('Port')).toBeDisabled();
    expect(screen.getByLabelText('Credential')).toBeDisabled();
    expect(screen.getByLabelText(/Inherit/)).toBeDisabled();
    expect(screen.getByLabelText(/Direct connection/)).toBeDisabled();
    expect(screen.getByLabelText(/This jump host/)).toBeDisabled();
  });
});
