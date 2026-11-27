import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, chooseFromMenu, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

/** The check dialog's synthetic plugins (backlog 3.13): the five the
 * Pulsix server probes itself, each with its own parameter fields. */
describe('CheckDialog — synthetic plugins', () => {
  async function openNew(user: ReturnType<typeof userEvent.setup>, plugin?: string) {
    const posted: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted.push(req.body as Record<string, unknown>);
        return { check: check({ id: 'c9', plugin: 'http', origin: 'custom' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New check' }));
    if (plugin) await pickOption(screen, 'Plugin', { value: plugin });
    return posted;
  }

  it('offers the five plugins, each marked as running from the Pulsix server', async () => {
    const user = userEvent.setup();
    await openNew(user);
    await user.click(screen.getByRole('combobox', { name: 'Plugin' }));
    for (const label of ['Ping (ICMP)', 'TCP port', 'HTTP(S)', 'TLS certificate', 'DNS']) {
      const option = await screen.findByRole('option', { name: new RegExp(`^${label.replace(/[()]/g, '\\$&')}`) });
      expect(within(option).getByText('runs from the Pulsix server')).toBeInTheDocument();
    }
    expect(within(screen.getByRole('option', { name: /^CPU/ })).queryByText('runs from the Pulsix server')).not.toBeInTheDocument();
  });

  it('prefills the key with the plugin name and says rules apply to that key', async () => {
    const user = userEvent.setup();
    await openNew(user, 'http');
    expect(screen.getByLabelText('Key')).toHaveValue('http');
    expect(screen.getByText(/Runs from the Pulsix server, not on the host/)).toBeInTheDocument();
    expect(screen.getByText('Alert rules on http.* metrics apply to every check of this type, whatever its key.')).toBeInTheDocument();
    await pickOption(screen, 'Plugin', { value: 'cpu' });
    expect(screen.getByLabelText('Key')).toHaveValue('');
    expect(screen.queryByText(/Alert rules on/)).not.toBeInTheDocument();
  });

  it('sends only the http fields the user set', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'http');
    await user.type(screen.getByLabelText('URL'), 'https://{{address}/health');
    await user.type(screen.getByLabelText('Body contains'), 'ok');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({ plugin: 'http', key: 'http' });
    expect(posted[0].params).toEqual({ url: 'https://{address}/health', body_contains: 'ok' });
  });

  it('does not submit tcp without a port and says why at the field', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'tcp');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('required')).toBeInTheDocument();
    expect(screen.getByLabelText('Port')).toHaveAttribute('aria-invalid', 'true');
    expect(posted).toHaveLength(0);
    await user.type(screen.getByLabelText('Port'), '22');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ port: 22 });
  });

  it('sends a line ending typed as \\r\\n in tcp send, and says how', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'tcp');
    expect(screen.getByText(/Use \\r\\n for a line break/)).toBeInTheDocument();
    await user.type(screen.getByLabelText('Port'), '6379');
    await user.type(screen.getByLabelText('Send after connecting'), 'PING\\r\\n');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ port: 6379, send: 'PING\r\n' });
  });

  it('leaves the number fields to its own validation, not the browser bubble', async () => {
    const user = userEvent.setup();
    await openNew(user, 'tcp');
    for (const label of ['Port', 'Timeout (seconds)']) {
      const input = screen.getByLabelText(label);
      expect(input).not.toHaveAttribute('min');
      expect(input).not.toHaveAttribute('max');
      expect(input).not.toHaveAttribute('step');
    }
    await user.type(screen.getByLabelText('Port'), '70000');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('must be a whole number between 1 and 65535')).toBeInTheDocument();
  });

  it('sends the dns record type the user picked', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'dns');
    await pickOption(screen, 'Record type', { value: 'AAAA' });
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({ type: 'AAAA' });
  });

  it('sends {} for icmp left at its defaults', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'icmp');
    expect(screen.getByLabelText('Pings per run')).toHaveAttribute('placeholder', '3');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].params).toEqual({});
  });

  it("prefills an existing http check's fields on edit and saves them unchanged", async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    const params = { url: 'https://example.org/', method: 'HEAD', follow_redirects: false, timeout_s: 20 };
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c8', key: 'http', plugin: 'http', origin: 'custom', params })] },
      'PUT /api/v1/orgs/acme/checks/c8': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c8', key: 'http', plugin: 'http', origin: 'custom' }) };
      },
    });
    renderPage();
    await chooseFromMenu(user, 'http (Organization)', 'Edit');
    expect(await screen.findByLabelText('URL')).toHaveValue('https://example.org/');
    expect(screen.getByRole('combobox', { name: 'Method' })).toHaveValue('HEAD');
    expect(screen.getByRole('switch', { name: 'Follow redirects (up to 10)' })).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByRole('switch', { name: 'Verify the TLS certificate' })).toHaveAttribute('aria-checked', 'true');
    expect(screen.getByLabelText('Timeout (seconds)')).toHaveValue(20);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put).toMatchObject({ key: 'http', plugin: 'http' });
    expect(put?.params).toEqual(params);
  });

  it('hints at the unit naming of all three systems on the service plugin', async () => {
    const user = userEvent.setup();
    await openNew(user, 'service');
    expect(
      screen.getByText('systemd units on Linux, service names on Windows, launchd labels on macOS – one per line'),
    ).toBeInTheDocument();
  });
});
