import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { pickOption } from '../../test/pickOption.ts';
import type { Host } from '../../api/types.ts';
import { HostDialog } from './HostDialog.tsx';

const ZONES = {
  zones: [
    { id: 'z1', slug: 'fra', name: 'Frankfurt', bastion_id: 'b1' },
    { id: 'z2', slug: 'ams', name: 'Amsterdam' },
  ],
};
const bastion = (id: string, name: string) => ({ id, name, address: `${name}.example.com`, port: 22, username: 'ops' });
const BASTIONS = { bastions: [bastion('b1', 'fra-jump'), bastion('b3', 'ams-jump')] };
const HOST: Host = {
  id: 'h1',
  name: 'web-04.fra',
  zone_id: 'z1',
  address: '203.0.113.4',
  managed: true,
  monitored: false,
  facts: {},
  revision: 7,
  created_at: '2026-01-01T00:00:00Z',
};
const SSH = { ssh: { host_id: 'h1', port: 22, username: '', bastion_mode: 'inherit' } };
const PREVIEW = { vars: {}, warnings: [], connection: { host_id: 'h1', bastion_source: 'zone', bastions: ['b3'] } };

function baseRoutes() {
  return {
    'GET /api/v1/orgs/acme/zones': ZONES,
    'GET /api/v1/orgs/acme/bastions': BASTIONS,
    'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    'GET /api/v1/orgs/acme/hosts/h1': { host: HOST },
    'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [{ key: 'env', value: 'prod', source: 'manual' }] },
    'GET /api/v1/orgs/acme/hosts/h1/ssh': SSH,
    'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': PREVIEW,
  };
}

function renderDialog({ role = 'admin', ...props }: { host?: Host; role?: string } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onOpenChange = vi.fn();
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <HostDialog org="acme" role={role} open onOpenChange={onOpenChange} {...props} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { onOpenChange };
}

const problem = (status: number, detail: string) =>
  new Response(JSON.stringify({ title: 'x', status, detail }), { status, headers: { 'Content-Type': 'application/problem+json' } });

describe('HostDialog', () => {
  it('creates a host with jump and tags in one request', async () => {
    const user = userEvent.setup();
    const posts: MockRequest[] = [];
    mockApi({
      ...baseRoutes(),
      'POST /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        posts.push(req);
        return { host: { ...HOST, id: 'h9', name: 'web-09.fra' } };
      },
    });
    const { onOpenChange } = renderDialog();

    await user.type(await screen.findByLabelText('Name'), 'web-09.fra');
    await pickOption(screen, 'Zone', { value: 'z1' });
    await pickOption(screen, 'Jump host', { value: 'b3' });
    await user.type(screen.getByLabelText('Tags'), 'role=db{Enter}');
    await user.click(screen.getByRole('button', { name: 'Create host' }));

    expect(await screen.findByText('Host web-09.fra created')).toBeInTheDocument();
    expect(posts).toHaveLength(1);
    expect(posts[0].body).toMatchObject({
      name: 'web-09.fra',
      zone_id: 'z1',
      jump: { mode: 'explicit', bastion_id: 'b3' },
      tags: [{ key: 'role', value: 'db' }],
    });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it('includes a tag still typed in the field when saving', async () => {
    const user = userEvent.setup();
    let body: unknown;
    mockApi({
      ...baseRoutes(),
      'POST /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        body = req.body;
        return { host: HOST };
      },
    });
    renderDialog();

    await user.type(await screen.findByLabelText('Name'), 'web-09.fra');
    await pickOption(screen, 'Zone', { value: 'z1' });
    await user.type(screen.getByLabelText('Tags'), 'role=db');
    await user.click(screen.getByRole('button', { name: 'Create host' }));

    await waitFor(() => expect(body).toMatchObject({ tags: [{ key: 'role', value: 'db' }] }));
    // Untouched jump host: left to inherit, so it is not sent at all.
    expect(body).not.toHaveProperty('jump');
  });

  it('edit prefills and PATCHes with If-Match', async () => {
    const user = userEvent.setup();
    let patch: MockRequest | undefined;
    mockApi({
      ...baseRoutes(),
      'PATCH /api/v1/orgs/acme/hosts/h1': (req: MockRequest) => {
        patch = req;
        return { host: { ...HOST, address: '203.0.113.99', revision: 8 } };
      },
    });
    renderDialog({ host: HOST });

    const name = await screen.findByLabelText('Name');
    expect(name).toHaveValue('web-04.fra');
    expect(screen.getByLabelText('Address')).toHaveValue('203.0.113.4');
    expect(screen.getByLabelText('Zone')).toHaveValue('Frankfurt');
    expect(await screen.findByText('env=prod')).toBeInTheDocument();

    await user.clear(screen.getByLabelText('Address'));
    await user.type(screen.getByLabelText('Address'), '203.0.113.99');
    await user.click(screen.getByRole('button', { name: 'Save host' }));

    await waitFor(() => expect(patch).toBeDefined());
    expect(patch?.headers.get('If-Match')).toBe('"7"');
    expect(patch?.body).toMatchObject({ name: 'web-04.fra', zone_id: 'z1', address: '203.0.113.99', managed: true, monitored: false });
    expect(patch?.body).not.toHaveProperty('jump');
    expect(patch?.body).not.toHaveProperty('tags');
  });

  it('sends jump and tags on edit when they changed', async () => {
    const user = userEvent.setup();
    let patch: MockRequest | undefined;
    mockApi({
      ...baseRoutes(),
      'PATCH /api/v1/orgs/acme/hosts/h1': (req: MockRequest) => {
        patch = req;
        return { host: { ...HOST, revision: 8 } };
      },
    });
    renderDialog({ host: HOST });

    await screen.findByText('env=prod');
    await pickOption(screen, 'Jump host', 'Direct, no jump host');
    await user.click(screen.getByRole('button', { name: 'remove env' }));
    await user.click(screen.getByRole('button', { name: 'Save host' }));

    await waitFor(() => expect(patch).toBeDefined());
    expect(patch?.body).toMatchObject({ jump: { mode: 'none' }, tags: [] });
  });

  it('stale edit keeps input and explains', async () => {
    const user = userEvent.setup();
    let hostGets = 0;
    mockApi({
      ...baseRoutes(),
      'GET /api/v1/orgs/acme/hosts/h1': () => {
        hostGets += 1;
        return { host: { ...HOST, name: 'web-04-renamed.fra', revision: 9 } };
      },
      'PATCH /api/v1/orgs/acme/hosts/h1': () => problem(412, 'the host changed since you loaded it; reload and try again'),
    });
    const { onOpenChange } = renderDialog({ host: HOST });

    const name = await screen.findByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'mine.fra');
    await user.click(screen.getByRole('button', { name: 'Save host' }));

    expect(await screen.findByText('This host was changed meanwhile — reload it and apply your change again')).toBeInTheDocument();
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByLabelText('Name')).toHaveValue('mine.fra');

    await user.click(screen.getByRole('button', { name: 'Reload' }));

    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('web-04-renamed.fra'));
    expect(hostGets).toBeGreaterThan(0);
    expect(screen.queryByText(/changed meanwhile/)).not.toBeInTheDocument();
  });

  it("a non-412 save failure shows the server's message and keeps the dialog open", async () => {
    const user = userEvent.setup();
    mockApi({
      ...baseRoutes(),
      'PATCH /api/v1/orgs/acme/hosts/h1': () => problem(500, 'the store is unavailable'),
    });
    const { onOpenChange } = renderDialog({ host: HOST });

    await screen.findByLabelText('Name');
    await user.click(screen.getByRole('button', { name: 'Save host' }));

    expect(await screen.findByText('the store is unavailable')).toBeInTheDocument();
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.queryByText(/changed meanwhile/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
  });

  it('a failed reload after a 412 says the reload failed', async () => {
    const user = userEvent.setup();
    let reloading = false;
    mockApi({
      ...baseRoutes(),
      'GET /api/v1/orgs/acme/hosts/h1': () => (reloading ? problem(500, 'reload broke') : { host: HOST }),
      'PATCH /api/v1/orgs/acme/hosts/h1': () => problem(412, 'the host changed since you loaded it; reload and try again'),
    });
    renderDialog({ host: HOST });

    await screen.findByLabelText('Name');
    await user.click(screen.getByRole('button', { name: 'Save host' }));
    expect(await screen.findByText(/changed meanwhile/)).toBeInTheDocument();

    reloading = true;
    await user.click(screen.getByRole('button', { name: 'Reload' }));

    expect(await screen.findByText('Reloading failed')).toBeInTheDocument();
    expect(screen.getByText(/changed meanwhile/)).toBeInTheDocument();
  });

  it('inherit shows the resolved jump host when editing', async () => {
    mockApi(baseRoutes());
    renderDialog({ host: HOST });

    await screen.findByLabelText('Name');
    // The preview's chain (ams-jump) differs from the zone's default
    // (fra-jump): only the resolved route can produce this label.
    await waitFor(() => expect(screen.getByLabelText('Jump host')).toHaveValue('Inherited: ams-jump'));
    expect(screen.queryByText(/an inventory group can override it/)).not.toBeInTheDocument();
  });

  it('shows the resolved jump host even when the preview arrives after the ssh settings', async () => {
    mockApi(baseRoutes());
    const inner = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input, init) => {
      if (input instanceof Request && input.url.includes('/inventory/preview')) {
        await new Promise((resolve) => setTimeout(resolve, 60));
      }
      return inner(input, init);
    });
    renderDialog({ host: HOST });

    // The form is up (ssh, tags and zones are in) before the preview is.
    await screen.findByLabelText('Name');
    await waitFor(() => expect(screen.getByLabelText('Jump host')).toHaveValue('Inherited: ams-jump'));
    expect(screen.queryByText(/an inventory group can override it/)).not.toBeInTheDocument();
  });

  it('inherit names the zone jump host and the group hint on create', async () => {
    const user = userEvent.setup();
    mockApi(baseRoutes());
    renderDialog();

    await screen.findByLabelText('Name');
    await pickOption(screen, 'Zone', { value: 'z1' });
    expect(screen.getByLabelText('Jump host')).toHaveValue('Inherited: fra-jump');
    expect(screen.getByText(/an inventory group can override it/)).toBeInTheDocument();

    await pickOption(screen, 'Zone', { value: 'z2' });
    expect(screen.getByLabelText('Jump host')).toHaveValue('Inherited: no jump host');
    await user.click(screen.getByLabelText('Jump host'));
    expect(screen.getByRole('option', { name: /Direct, no jump host/ })).toBeInTheDocument();
  });

  it('zone change shows the workers hint', async () => {
    mockApi(baseRoutes());
    renderDialog({ host: HOST });

    await screen.findByLabelText('Name');
    expect(screen.queryByText(/workers of zone/)).not.toBeInTheDocument();
    await pickOption(screen, 'Zone', { value: 'z2' });

    expect(screen.getByText('Future runs for this host go to the workers of zone ams.')).toBeInTheDocument();
    // The resolved route belongs to the old zone, so it is no longer shown.
    expect(screen.getByLabelText('Jump host')).toHaveValue('Inherited: no jump host');
  });

  // The caller already holds the organization and passes the role down; the
  // dialog does not read the organization a second time.
  it.each([
    ['admin', true],
    ['viewer', false],
  ])('asks nothing about the organization (%s)', async (role, offersJump) => {
    const calls = mockApi(baseRoutes());
    calls.mockClear();
    renderDialog({ role });

    await screen.findByLabelText('Name');
    if (offersJump) expect(screen.getByLabelText('Jump host')).toBeInTheDocument();
    else expect(screen.queryByLabelText('Jump host')).not.toBeInTheDocument();
    const requested = calls.mock.calls.map(([input]) => new URL((input as Request).url).pathname);
    expect(requested).not.toContain('/api/v1/orgs/acme');
  });

  it('no inventory.write hides the jump field', async () => {
    const user = userEvent.setup();
    let body: unknown;
    mockApi({
      ...baseRoutes(),
      'POST /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        body = req.body;
        return { host: HOST };
      },
    });
    renderDialog({ role: 'viewer' });

    await user.type(await screen.findByLabelText('Name'), 'web-09.fra');
    expect(screen.queryByLabelText('Jump host')).not.toBeInTheDocument();
    await pickOption(screen, 'Zone', { value: 'z1' });
    await user.click(screen.getByRole('button', { name: 'Create host' }));

    await waitFor(() => expect(body).toBeDefined());
    expect(body).not.toHaveProperty('jump');
  });

  it('no credentials.write hides create jump host', async () => {
    const user = userEvent.setup();
    mockApi(baseRoutes());
    renderDialog({ role: 'operator' });

    await screen.findByLabelText('Name');
    await user.click(await screen.findByLabelText('Jump host'));
    expect(screen.getByRole('option', { name: /fra-jump/ })).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: /Create jump host/ })).not.toBeInTheDocument();
  });

  it('creating a jump host from the dialog preselects it', async () => {
    const user = userEvent.setup();
    let created = false;
    mockApi({
      ...baseRoutes(),
      'GET /api/v1/orgs/acme/bastions': () => ({ bastions: created ? [...BASTIONS.bastions, bastion('b9', 'new-jump')] : BASTIONS.bastions }),
      'POST /api/v1/orgs/acme/bastions': () => {
        created = true;
        return { bastion: bastion('b9', 'new-jump') };
      },
    });
    renderDialog();

    await screen.findByLabelText('Name');
    await user.click(await screen.findByLabelText('Jump host'));
    await user.click(screen.getByRole('option', { name: /Create jump host/ }));

    const dialog = await screen.findByRole('dialog', { name: 'Add jump host' });
    await user.type(within(dialog).getByLabelText('Name'), 'new-jump');
    await user.type(within(dialog).getByLabelText('Address'), 'new-jump.example.com');
    await user.type(within(dialog).getByLabelText('Username'), 'ops');
    await user.click(within(dialog).getByRole('button', { name: 'Create jump host' }));

    await waitFor(() => expect(screen.getByLabelText('Jump host')).toHaveValue('new-jump'));
  });

  it('capabilities are switches', async () => {
    mockApi(baseRoutes());
    renderDialog();

    await screen.findByLabelText('Name');
    expect(screen.getAllByRole('switch')).toHaveLength(2);
    expect(screen.getByRole('switch', { name: 'Managed (Ansible via SSH)' })).toHaveAccessibleDescription(/SSH/);
  });

  it('keeps the zone-create row in the zone picker', async () => {
    const user = userEvent.setup();
    mockApi(baseRoutes());
    renderDialog();

    await screen.findByLabelText('Name');
    await user.click(screen.getByLabelText('Zone'));
    expect(screen.getByRole('option', { name: /New zone/ })).toBeInTheDocument();
  });
});
