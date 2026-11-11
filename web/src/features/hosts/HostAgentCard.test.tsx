import { describe, expect, it } from 'vitest';
import userEvent from '@testing-library/user-event';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { HostAgentCard } from './HostAgentCard.tsx';

const inDays = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString();

function renderCard(canWrite = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <HostAgentCard org="acme" hostId="h1" hostName="web-1" zoneId="z1" canWrite={canWrite} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('HostAgentCard', () => {
  it('shows the agent with its state, version and certificate expiry', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online',
          last_seen: new Date().toISOString(), version: 'v0.3a', cert_expires_at: inDays(60), created_at: '',
        },
      },
    });
    renderCard();

    expect(await screen.findByText('online')).toBeInTheDocument();
    expect(screen.getByText('v0.3a')).toBeInTheDocument();
    expect(screen.getByText('just now')).toBeInTheDocument();
    expect(screen.getByText('in 59 d')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Revoke agent on web-1' })).toBeInTheDocument();
  });

  it.each([
    ['update_required', 'Update required'],
    ['newer', 'Newer than manager'],
  ])('badges a %s agent', async (versionStatus, label) => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online', last_seen: new Date().toISOString(),
          version: 'v1.1.0', version_status: versionStatus, cert_expires_at: inDays(60), created_at: '',
        },
      },
    });
    renderCard();

    expect(await screen.findByText(label)).toBeInTheDocument();
    expect(screen.getByText('v1.1.0')).toBeInTheDocument();
  });

  it.each(['current', 'previous_minor', 'unknown'])('shows no version badge for a %s agent', async (versionStatus) => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online', last_seen: new Date().toISOString(),
          version: 'v1.3.0', version_status: versionStatus, cert_expires_at: inDays(60), created_at: '',
        },
      },
    });
    renderCard();

    expect(await screen.findByText('v1.3.0')).toBeInTheDocument();
    expect(screen.queryByText('Update required')).not.toBeInTheDocument();
    expect(screen.queryByText('Newer than manager')).not.toBeInTheDocument();
  });

  it('highlights a certificate that is about to expire', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'offline',
          last_seen: null, version: '', cert_expires_at: inDays(3), created_at: '',
        },
      },
    });
    renderCard();

    expect(await screen.findByText('offline')).toBeInTheDocument();
    expect(screen.getByText('never')).toBeInTheDocument();
    expect(document.querySelector('[data-expiring]')?.textContent).toMatch(/in [23] d/);
  });

  it('tells an operator how to enrol one when the host has no agent', async () => {
    // The read answers 404 for a host without an agent: a state to
    // render, not an error to retry.
    mockApi({ 'GET /api/v1/orgs/acme/hosts/h1/agent': { status: 404, body: { title: 'Not Found', status: 404 } } });
    renderCard();

    expect(await screen.findByText(/No agent on this host/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Revoke/ })).not.toBeInTheDocument();
  });

  it('offers a viewer no revoke action', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online',
          last_seen: new Date().toISOString(), version: 'v0.3a', cert_expires_at: inDays(60), created_at: '',
        },
      },
    });
    renderCard(false);

    expect(await screen.findByText('online')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Revoke/ })).not.toBeInTheDocument();
  });

  it('offers Enrol agent on a host without one and opens the dialog for that host', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': { status: 404, body: { title: 'Not Found', status: 404 } },
      'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', org_id: 'o', slug: 'hel', name: 'Helsinki' }] },
    });
    renderCard();

    await user.click(await screen.findByRole('button', { name: 'Enrol agent' }));
    expect(screen.getByText('Enrol an agent on web-1')).toBeInTheDocument();
  });

  it('offers no Enrol agent to a viewer or on a host that has an agent', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/hosts/h1/agent': { status: 404, body: { title: 'Not Found', status: 404 } } });
    const { unmount } = renderCard(false);
    expect(await screen.findByText(/No agent on this host/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Enrol agent' })).not.toBeInTheDocument();
    unmount();

    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online',
          last_seen: new Date().toISOString(), version: 'v0.3a', cert_expires_at: inDays(60), created_at: '',
        },
      },
    });
    renderCard();
    expect(await screen.findByText('online')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Enrol agent' })).not.toBeInTheDocument();
  });
});
