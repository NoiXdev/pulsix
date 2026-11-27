import { describe, expect, it } from 'vitest';
import userEvent from '@testing-library/user-event';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi } from '../../test/mockApi.ts';
import { HostAgentCard } from './HostAgentCard.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';

const inDays = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString();

function renderCard(canWrite = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <HostAgentCard org="acme" hostId="h1" hostName="web-1" zoneId="z1" canWrite={canWrite} />
        </ToastProvider>
      </ConfirmProvider>
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

  it.each([
    ['aaaaaaaa1111', 'Config current', 'Agent runs aaaaaaaa · manager expects aaaaaaaa'],
    ['bbbbbbbb2222', 'Config outdated', 'Agent runs aaaaaaaa · manager expects bbbbbbbb'],
  ])('badges the configuration check against %s', async (expected, label, detail) => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online', last_seen: new Date().toISOString(),
          version: 'v1.1.0', cert_expires_at: inDays(60), created_at: '',
          config_version: 'aaaaaaaa1111', config_expected: expected, config_checked_at: new Date().toISOString(),
        },
      },
    });
    renderCard();

    expect(await screen.findByText(label)).toBeInTheDocument();
    expect(screen.getByTitle((title) => title.startsWith(`${detail} · checked `))).toBeInTheDocument();
    // The card has the room to show the versions of an outdated agent as
    // text; a current one needs no detail.
    if (label === 'Config outdated') expect(screen.getByText(detail)).toBeInTheDocument();
    else expect(screen.queryByText(detail)).toBeNull();
  });

  it('wraps the version row when both badges show', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online', last_seen: new Date().toISOString(),
          version: 'v1.1.0', version_status: 'update_required', cert_expires_at: inDays(60), created_at: '',
          config_version: 'aaaaaaaa', config_expected: 'bbbbbbbb', config_checked_at: new Date().toISOString(),
        },
      },
    });
    renderCard();

    const cell = (await screen.findByText('Update required')).closest('span.flex');
    expect(cell).toContainElement(screen.getByText('Config outdated'));
    expect(cell).toHaveClass('flex-wrap');
  });

  it('shows no configuration badge before the manager has compared', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/hosts/h1/agent': {
        agent: {
          host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online', last_seen: new Date().toISOString(),
          version: 'v1.1.0', cert_expires_at: inDays(60), created_at: '',
        },
      },
    });
    renderCard();

    expect(await screen.findByText('v1.1.0')).toBeInTheDocument();
    expect(screen.queryByText(/^Config /)).toBeNull();
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

  describe('probes for other hosts', () => {
    const withProbes = (supported: boolean) =>
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/agent': {
          agent: {
            host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online',
            last_seen: new Date().toISOString(), version: 'v0.3a', created_at: '', probes_supported: supported,
          },
        },
      });

    it('says when the agent probes other hosts', async () => {
      withProbes(true);
      renderCard();
      expect(await screen.findByText('Probes other hosts')).toBeInTheDocument();
      expect(screen.getByText('Yes')).toBeInTheDocument();
    });

    it('says why an agent may not probe other hosts', async () => {
      withProbes(false);
      renderCard();
      expect(await screen.findByText('Probes other hosts')).toBeInTheDocument();
      expect(screen.getByText('No — turned off in agent.yaml, or the agent is too old')).toBeInTheDocument();
    });
  });

  describe('secrets report', () => {
    const withSecrets = (over: Record<string, unknown>) =>
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/agent': {
          agent: {
            host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online',
            last_seen: new Date().toISOString(), version: 'v0.3a', created_at: '', ...over,
          },
        },
      });

    it('lists the secret names when agent.yaml loaded', async () => {
      withSecrets({ secrets_state: 'ok', secret_names: ['db', 'mysql-prod'] });
      renderCard();
      expect(await screen.findByText('agent.yaml: 2 secrets')).toBeInTheDocument();
      expect(screen.getByText('db')).toBeInTheDocument();
      expect(screen.getByText('mysql-prod')).toBeInTheDocument();
    });

    it('says when there is no agent.yaml', async () => {
      withSecrets({ secrets_state: 'absent', secret_names: [] });
      renderCard();
      expect(await screen.findByText('No agent.yaml')).toBeInTheDocument();
    });

    it('warns with the error text when agent.yaml is broken', async () => {
      withSecrets({ secrets_state: 'agent.yaml: line 3: bad indent', secret_names: [] });
      renderCard();
      expect(await screen.findByText('agent.yaml: line 3: bad indent')).toBeInTheDocument();
    });

    it('wraps a long error text and long names instead of overflowing a phone screen', async () => {
      const path = 'C:\\ProgramData\\Pulsix\\' + 'x'.repeat(120) + '\\agent.yaml';
      withSecrets({ secrets_state: `configuration file ${path} holds secrets but is accessible to owner PC\\mallory; restrict it`, secret_names: [] });
      renderCard();
      expect(await screen.findByRole('alert')).toHaveClass('wrap-anywhere');
    });

    it('lets a long secret name wrap', async () => {
      withSecrets({ secrets_state: 'ok', secret_names: ['n'.repeat(64)] });
      renderCard();
      expect(await screen.findByText('n'.repeat(64))).toHaveClass('break-all');
    });

    it('says it is unknown when the agent has not reported', async () => {
      withSecrets({ secrets_state: null, secret_names: null });
      renderCard();
      expect(await screen.findByText(/Unknown — the agent has not reported it yet/)).toBeInTheDocument();
    });
  });

  // Backlog 3.5a: the checks agent.yaml defines that the agent does not
  // run, one line each as it reported them.
  describe('local check problems', () => {
    const withProblems = (problems: string[] | null) =>
      mockApi({
        'GET /api/v1/orgs/acme/hosts/h1/agent': {
          agent: {
            host_id: 'h1', host_name: 'web-1', agent_name: 'web-1', status: 'online', last_seen: new Date().toISOString(),
            version: 'v0.3a', created_at: '', secrets_state: 'ok', secret_names: [], local_check_problems: problems,
          },
        },
      });

    it('lists each problem under its heading', async () => {
      withProblems(['agent.yaml: line 14: checks[1]: unknown plugin "nope"', 'agent.yaml: line 20: checks[2]: key "app" is used twice']);
      renderCard();
      expect(await screen.findByText('Problems with local checks')).toBeInTheDocument();
      const list = screen.getByRole('list', { name: 'Problems with local checks' });
      expect(within(list).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
        'agent.yaml: line 14: checks[1]: unknown plugin "nope"',
        'agent.yaml: line 20: checks[2]: key "app" is used twice',
      ]);
      expect(within(list).getAllByRole('listitem')[0]).toHaveClass('wrap-anywhere');
    });

    it.each([
      ['none', [] as string[]],
      ['unknown', null],
    ])('says nothing when there are %s', async (_name, problems) => {
      withProblems(problems);
      renderCard();
      expect(await screen.findByText('online')).toBeInTheDocument();
      expect(screen.queryByText('Problems with local checks')).not.toBeInTheDocument();
    });
  });
});
