import { describe, expect, it } from 'vitest';
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
        <HostAgentCard org="acme" hostId="h1" canWrite={canWrite} />
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
});
