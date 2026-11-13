import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, chooseFromMenu, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

/** The check dialog's nginx, apache, caddy and phpfpm plugins (backlog 3.11). */
const SINGLE = [
  { plugin: 'nginx', label: 'Status page URL', url: 'http://127.0.0.1/nginx_status' },
  { plugin: 'apache', label: 'Status page URL', url: 'http://127.0.0.1/server-status?auto' },
  { plugin: 'caddy', label: 'Metrics URL', url: 'http://127.0.0.1:2019/metrics' },
] as const;
const ALL = ['nginx', 'apache', 'caddy', 'phpfpm'] as const;

async function openNew(user: ReturnType<typeof userEvent.setup>, plugin: string) {
  const posted: Record<string, unknown>[] = [];
  mockApi({
    'GET /api/v1/orgs/acme': org('operator'),
    'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
    'GET /api/v1/orgs/acme/checks': { checks: [] },
    'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
      posted.push(req.body as Record<string, unknown>);
      return { check: check({ id: 'c9', plugin, origin: 'custom' }) };
    },
  });
  renderPage();
  await user.click(await screen.findByRole('button', { name: 'New check' }));
  await pickOption(screen, 'Plugin', { value: plugin });
  return posted;
}

async function save(user: ReturnType<typeof userEvent.setup>, posted: unknown[]) {
  await user.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(posted).toHaveLength(1));
}

describe.each(ALL)('CheckDialog — %s plugin', (plugin) => {
  it('is selectable and shows an optional secret with the agent.yaml hint', async () => {
    const user = userEvent.setup();
    await openNew(user, plugin);
    expect(screen.getByLabelText(/^Secret \(optional\)/)).toHaveAttribute('placeholder', 'status-login');
    expect(screen.getByText(/basic_auth_user, basic_auth_password/)).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Verify the TLS certificate' })).toHaveAttribute('aria-checked', 'true');
    expect(screen.queryByText(/Runs from the Pulsix server/)).not.toBeInTheDocument();
  });

  it('sends nothing but the secret when only that was entered', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, plugin);
    await user.type(screen.getByLabelText(/^Secret \(optional\)/), 'status-login');
    await save(user, posted);
    expect(posted[0].params).toEqual({ secret: 'status-login' });
  });

  it('refuses an invalid secret name and sends nothing', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, plugin);
    await user.type(screen.getByLabelText(/^Secret \(optional\)/), 'bad name!');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/must be 1 to 64 characters: letters, digits and \. _ -/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });

  it('sends verify_tls only once the switch was changed', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, plugin);
    await user.click(screen.getByRole('switch', { name: 'Verify the TLS certificate' }));
    await save(user, posted);
    expect(posted[0].params).toEqual({ verify_tls: false });
  });
});

describe.each(SINGLE)('CheckDialog — $plugin status URL', ({ plugin, label, url }) => {
  it('shows the default as placeholder and does not send it untouched', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, plugin);
    const field = screen.getByLabelText(label);
    expect(field).toHaveAttribute('placeholder', url);
    expect(field).toHaveValue('');
    await save(user, posted);
    expect(posted[0]).toMatchObject({ plugin });
    expect(posted[0].params).toEqual({});
  });

  it('sends an entered URL', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, plugin);
    await user.type(screen.getByLabelText(label), 'https://status.example/x');
    await save(user, posted);
    expect(posted[0].params).toEqual({ url: 'https://status.example/x' });
  });

  it.each(['ftp://x/', 'http://u:p@h/', 'http://{address}/', 'http://h/#frag'])('refuses %s and sends nothing', async (bad) => {
    const user = userEvent.setup();
    const posted = await openNew(user, plugin);
    await user.type(screen.getByLabelText(label), bad.replace(/\{/g, '{{'));
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/must be an http:\/\/ or https:\/\/ URL without a user name, password or #fragment/)).toBeInTheDocument();
    expect(screen.getByLabelText(label)).toHaveAttribute('aria-invalid', 'true');
    expect(posted).toHaveLength(0);
  });

  it('prefills a stored check on edit and saves it unchanged', async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    const params = { url: 'https://status.example/x', secret: 'status-login', verify_tls: false };
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c8', key: plugin, plugin, origin: 'custom', params })] },
      'PUT /api/v1/orgs/acme/checks/c8': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c8', key: plugin, plugin, origin: 'custom' }) };
      },
    });
    renderPage();
    await chooseFromMenu(user, `${plugin} (Organization)`, 'Edit');
    expect(await screen.findByLabelText(label)).toHaveValue('https://status.example/x');
    expect(screen.getByLabelText(/^Secret \(optional\)/)).toHaveValue('status-login');
    expect(screen.getByRole('switch', { name: 'Verify the TLS certificate' })).toHaveAttribute('aria-checked', 'false');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.params).toEqual(params);
  });
});

describe('CheckDialog — phpfpm status URLs list', () => {
  const LABEL = 'Status page URLs (one per line, one pool each)';

  it('shows the example as placeholder and sends nothing untouched', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'phpfpm');
    const field = screen.getByLabelText(LABEL);
    expect(field).toHaveAttribute('placeholder', 'http://127.0.0.1/status');
    expect(field).toHaveValue('');
    expect(screen.getByText(/Empty means one pool at http:\/\/127\.0\.0\.1\/status/)).toBeInTheDocument();
    await save(user, posted);
    expect(posted[0].params).toEqual({});
  });

  it('sends the entered lines as an array', async () => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'phpfpm');
    await user.type(screen.getByLabelText(LABEL), 'http://127.0.0.1/status-a\nhttp://127.0.0.1/status-b');
    await save(user, posted);
    expect(posted[0].params).toEqual({ urls: ['http://127.0.0.1/status-a', 'http://127.0.0.1/status-b'] });
  });

  it.each(['ftp://x/', 'http://u:p@h/', 'http://{address}/'])('refuses %s among the lines and sends nothing', async (bad) => {
    const user = userEvent.setup();
    const posted = await openNew(user, 'phpfpm');
    await user.type(screen.getByLabelText(LABEL), `http://127.0.0.1/status\n${bad.replace(/\{/g, '{{')}`);
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/must be an http:\/\/ or https:\/\/ URL without a user name, password or #fragment/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });

  it('prefills the stored pools on edit and saves them unchanged', async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    const params = { urls: ['http://127.0.0.1/status-a', 'http://127.0.0.1/status-b'], secret: 'status-login' };
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c8', key: 'phpfpm', plugin: 'phpfpm', origin: 'custom', params })] },
      'PUT /api/v1/orgs/acme/checks/c8': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c8', key: 'phpfpm', plugin: 'phpfpm', origin: 'custom' }) };
      },
    });
    renderPage();
    await chooseFromMenu(user, 'phpfpm (Organization)', 'Edit');
    expect(await screen.findByLabelText(LABEL)).toHaveValue('http://127.0.0.1/status-a\nhttp://127.0.0.1/status-b');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put?.params).toEqual(params);
  });
});
