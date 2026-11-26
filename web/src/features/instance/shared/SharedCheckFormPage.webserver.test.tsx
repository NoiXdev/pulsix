import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedCheckFormPage } from './SharedCheckFormPage.tsx';

/** The shared check form's nginx, apache, caddy and phpfpm plugins (backlog 3.11). */
const SINGLE = [
  { plugin: 'nginx', label: 'Status page URL', url: 'http://127.0.0.1/nginx_status' },
  { plugin: 'apache', label: 'Status page URL', url: 'http://127.0.0.1/server-status?auto' },
  { plugin: 'caddy', label: 'Metrics URL', url: 'http://127.0.0.1:2019/metrics' },
] as const;
const ALL = ['nginx', 'apache', 'caddy', 'phpfpm'] as const;
const SECRET = /^Secret \(optional\)/;
const LIST = 'Status pages (one per line, one pool each)';

async function openNew(plugin: string) {
  const posted = mockCreate();
  renderAt('/o/acme/settings/shared/checks/new');
  await screen.findByLabelText('Key');
  await pickOption(screen, 'Plugin', { value: plugin });
  return posted;
}

function mockCreate() {
  const posted: Record<string, unknown>[] = [];
  mockApi({
    'GET /api/v1/auth/me': ADMIN,
    'GET /api/v1/admin/checks/definitions': { definitions: [] },
    'POST /api/v1/admin/checks/definitions': (req: MockRequest) => {
      posted.push(req.body as Record<string, unknown>);
      return { definition: { id: 'c9', ...(req.body as Record<string, unknown>) } };
    },
  });
  return posted;
}

function renderAt(path: string) {
  renderRoutes(
    [
      { path: '/settings/shared/checks', component: () => <div>the list</div> },
      { path: '/settings/shared/checks/new', component: SharedCheckFormPage },
      { path: '/settings/shared/checks/$id', component: SharedCheckFormPage },
    ],
    path,
  );
}

async function create(user: ReturnType<typeof userEvent.setup>, posted: unknown[]) {
  await user.click(screen.getByRole('button', { name: 'Create definition' }));
  await waitFor(() => expect(posted).toHaveLength(1));
}

describe.each(ALL)('SharedCheckFormPage — %s plugin', (plugin) => {
  it('is selectable and shows an optional secret with the agent.yaml hint', async () => {
    await openNew(plugin);
    expect(screen.getByLabelText(SECRET)).toHaveAttribute('placeholder', 'status-login');
    expect(screen.getByText(/basic_auth_user, basic_auth_password/)).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Verify the TLS certificate' })).toHaveAttribute('aria-checked', 'true');
  });

  it('accepts an empty form and sends no params', async () => {
    const user = userEvent.setup();
    const posted = await openNew(plugin);
    await create(user, posted);
    expect(posted[0]).toMatchObject({ plugin });
    expect(posted[0].params).toEqual({});
  });

  it('sends the secret and verify_tls only once changed', async () => {
    const user = userEvent.setup();
    const posted = await openNew(plugin);
    await user.type(screen.getByLabelText(SECRET), 'status-login');
    await user.click(screen.getByRole('switch', { name: 'Verify the TLS certificate' }));
    await create(user, posted);
    expect(posted[0].params).toEqual({ secret: 'status-login', verify_tls: false });
  });

  it('refuses an invalid secret name and sends nothing', async () => {
    const user = userEvent.setup();
    const posted = await openNew(plugin);
    await user.type(screen.getByLabelText(SECRET), 'bad name!');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    await waitFor(() => expect(screen.getByLabelText(SECRET)).toHaveAttribute('aria-invalid', 'true'));
    expect(posted).toHaveLength(0);
  });
});

describe.each(SINGLE)('SharedCheckFormPage — $plugin status URL', ({ plugin, label, url }) => {
  it('shows the default as placeholder and does not send it untouched', async () => {
    const user = userEvent.setup();
    const posted = await openNew(plugin);
    expect(screen.getByLabelText(label)).toHaveAttribute('placeholder', url);
    await create(user, posted);
    expect(posted[0].params).toEqual({});
  });

  it('sends an entered URL', async () => {
    const user = userEvent.setup();
    const posted = await openNew(plugin);
    await user.type(screen.getByLabelText(label), 'https://status.example/x');
    await create(user, posted);
    expect(posted[0].params).toEqual({ url: 'https://status.example/x' });
  });

  it.each(['ftp://x/', 'http://u:p@h/', 'http://{address}/', 'http://h/#frag'])('refuses %s and sends nothing', async (bad) => {
    const user = userEvent.setup();
    const posted = await openNew(plugin);
    await user.type(screen.getByLabelText(label), bad.replace(/\{/g, '{{'));
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByText(/must be an http:\/\/ or https:\/\/ URL without a user name, password or #fragment/)).toBeInTheDocument();
    expect(screen.getByLabelText(label)).toHaveAttribute('aria-invalid', 'true');
    expect(posted).toHaveLength(0);
  });
});

const def = (plugin: string, params: Record<string, unknown>) => ({ id: 'c1', key: plugin, plugin, params, interval_seconds: 60 });

async function openEdit(definition: ReturnType<typeof def>) {
  const puts: unknown[] = [];
  mockApi({
    'GET /api/v1/auth/me': ADMIN,
    'GET /api/v1/admin/checks/definitions': { definitions: [definition] },
    'PUT /api/v1/admin/checks/definitions/c1': (req: MockRequest) => {
      puts.push(req.body);
      return { definition };
    },
  });
  renderAt('/o/acme/settings/shared/checks/c1');
  return puts;
}

describe('SharedCheckFormPage — web server definitions on edit', () => {
  it('prefills a phpfpm definition and saves it unchanged', async () => {
    const user = userEvent.setup();
    const params = { urls: ['http://127.0.0.1/status-a', 'http://127.0.0.1/status-b'], secret: 'status-login' };
    const puts = await openEdit(def('phpfpm', params));
    expect(await screen.findByLabelText(LIST)).toHaveValue('http://127.0.0.1/status-a\nhttp://127.0.0.1/status-b');
    expect(screen.getByLabelText(SECRET)).toHaveValue('status-login');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ key: 'phpfpm', plugin: 'phpfpm', interval_seconds: 60, params });
  });

  it('prefills an nginx definition with secret and verify_tls and saves it unchanged', async () => {
    const user = userEvent.setup();
    const params = { url: 'https://status.example/x', secret: 'status-login', verify_tls: false };
    const puts = await openEdit(def('nginx', params));
    expect(await screen.findByLabelText('Status page URL')).toHaveValue('https://status.example/x');
    expect(screen.getByLabelText(SECRET)).toHaveValue('status-login');
    expect(screen.getByRole('switch', { name: 'Verify the TLS certificate' })).toHaveAttribute('aria-checked', 'false');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ key: 'nginx', plugin: 'nginx', interval_seconds: 60, params });
  });
});

describe('SharedCheckFormPage — phpfpm status URLs list', () => {
  it('shows the example as placeholder and sends nothing untouched', async () => {
    const user = userEvent.setup();
    const posted = await openNew('phpfpm');
    expect(screen.getByLabelText(LIST)).toHaveAttribute('placeholder', 'http://127.0.0.1/status');
    await create(user, posted);
    expect(posted[0].params).toEqual({});
  });

  it('sends the entered lines as an array', async () => {
    const user = userEvent.setup();
    const posted = await openNew('phpfpm');
    await user.type(screen.getByLabelText(LIST), 'http://127.0.0.1/status-a\nhttp://127.0.0.1/status-b');
    await create(user, posted);
    expect(posted[0].params).toEqual({ urls: ['http://127.0.0.1/status-a', 'http://127.0.0.1/status-b'] });
  });

  it.each(['ftp://x/', 'http://u:p@h/', 'http://{address}/'])('refuses %s among the lines and sends nothing', async (bad) => {
    const user = userEvent.setup();
    const posted = await openNew('phpfpm');
    await user.type(screen.getByLabelText(LIST), `http://127.0.0.1/status\n${bad.replace(/\{/g, '{{')}`);
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByText(/must be an http:\/\/ or https:\/\/ URL without a user name, password or #fragment/)).toBeInTheDocument();
    expect(posted).toHaveLength(0);
  });
});
