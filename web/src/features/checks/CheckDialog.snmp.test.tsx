import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

/** The check dialog's snmp plugin (backlog 3.13d): a probe the Pulsix
 * server runs against the host, its credentials in the organization's
 * secret store rather than in any agent.yaml. */
describe('CheckDialog — snmp plugin', () => {
  async function openNew(user: ReturnType<typeof userEvent.setup>, pick = true) {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { check: check({ id: 'c9', key: 'snmp', plugin: 'snmp', origin: 'custom' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    if (pick) await pickOption(screen, 'Plugin', { value: 'snmp' });
    return posted;
  }

  it('is offered in the plugin picker as running from the Pulsix server', async () => {
    const user = userEvent.setup();
    await openNew(user, false);
    await user.click(screen.getByRole('combobox', { name: 'Plugin' }));
    const option = await screen.findByRole('option', { name: /^SNMP device/ });
    expect(within(option).getByText('runs from the Pulsix server')).toBeInTheDocument();
  });

  it('shows its parameters, the secret pointing to Settings → Secrets rather than agent.yaml', async () => {
    const user = userEvent.setup();
    await openNew(user);
    expect(screen.getByLabelText('Secret')).toHaveAttribute('placeholder', 'switch-core');
    expect(screen.getByText(/under Settings → Secrets/)).toBeInTheDocument();
    expect(screen.queryByText(/agent\.yaml/)).not.toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: 'SNMP version' })).toHaveValue('v2c (community)');
    expect(screen.getByLabelText('Port')).toHaveAttribute('placeholder', '161');
    expect(screen.getByLabelText('Timeout per request (seconds)')).toBeInTheDocument();
    expect(screen.getByLabelText('Values (one per line, name=OID)')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Watch the interfaces' })).toBeInTheDocument();
    expect(screen.getByLabelText('Key')).toHaveValue('snmp');
  });

  it('refuses a missing secret and a malformed OID line, and sends nothing', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.type(screen.getByLabelText('Values (one per line, name=OID)'), 'ups_load=1.3.6.x');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('required')).toBeInTheDocument();
    expect(screen.getByLabelText('Secret')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByText(/every entry must be name=OID/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });

  it('sends the secret, the version, the OIDs and the interfaces switch', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user);
    await user.type(screen.getByLabelText('Secret'), 'switch-core');
    await pickOption(screen, 'SNMP version', { value: 'v3' });
    await user.type(
      screen.getByLabelText('Values (one per line, name=OID)'),
      'ups_load=1.3.6.1.2.1.33.1.4.4.1.5.1{Enter}uptime=1.3.6.1.2.1.1.3.0',
    );
    await user.click(screen.getByRole('switch', { name: 'Watch the interfaces' }));
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ plugin: 'snmp', key: 'snmp' });
    expect(posted[0].params).toEqual({
      secret: 'switch-core',
      version: 'v3',
      oids: ['ups_load=1.3.6.1.2.1.33.1.4.4.1.5.1', 'uptime=1.3.6.1.2.1.1.3.0'],
      interfaces: true,
    });
  });
});
