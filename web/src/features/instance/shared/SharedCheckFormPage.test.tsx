import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedCheckFormPage } from './SharedCheckFormPage.tsx';
import { settled } from '../../../test/settled.ts';

const LIST = '/o/acme/settings/shared/checks';

const checkDef = (over: Record<string, unknown> = {}) => ({
  id: 'c1', key: 'disk-data', plugin: 'disk', params: {}, interval_seconds: 300, ...over,
});

function renderPage(path: string) {
  return renderRoutes(
    [
      { path: '/settings/shared/checks', component: () => <div>the list</div> },
      { path: '/settings/shared/checks/new', component: SharedCheckFormPage },
      { path: '/settings/shared/checks/$id', component: SharedCheckFormPage },
    ],
    path,
  );
}

afterEach(() => vi.restoreAllMocks());

describe('SharedCheckFormPage — new', () => {
  it('posts a new definition and goes back to the list', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
      'POST /api/v1/admin/checks/definitions': (req: MockRequest) => {
        posted = req.body;
        return { definition: checkDef({ id: 'c9', ...(req.body as Record<string, unknown>) }) };
      },
    });
    const router = renderPage(`${LIST}/new`);
    await user.type(await screen.findByLabelText('Key'), 'cpu-watch');
    await pickOption(screen, 'Plugin', { value: 'memory' });
    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.type(screen.getByLabelText('Interval (seconds)'), '60');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));

    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
    expect(posted).toMatchObject({ key: 'cpu-watch', plugin: 'memory', interval_seconds: 60 });
    expect(await screen.findByText('Check definition memory created')).toBeInTheDocument();
  });

  it('does not submit while the required fields are empty', async () => {
    const user = userEvent.setup();
    let posts = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
      'POST /api/v1/admin/checks/definitions': () => {
        posts += 1;
        return { definition: checkDef() };
      },
    });
    renderPage(`${LIST}/new`);
    await screen.findByLabelText('Key');
    await pickOption(screen, 'Plugin', { value: 'service' });
    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    // Two refusals at once: the cleared interval, and "service" with no
    // units at all — the same shape internal/checks.validateParams enforces
    // server-side, refused client-side before the round trip here too.
    expect(await screen.findAllByText('required')).toHaveLength(2);
    expect(posts).toBe(0);
  });

  it('shows which organizations already use a key when the create collides', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
      'POST /api/v1/admin/checks/definitions': () =>
        new Response(JSON.stringify({ detail: 'name "cpu" is already used by acme, widget-co' }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage(`${LIST}/new`);
    await user.type(await screen.findByLabelText('Key'), 'cpu');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name "cpu" is already used by acme, widget-co');
  });

  it('asks before leaving with unsaved changes, and stays when declined', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
    });
    const router = renderPage(`${LIST}/new`);
    await user.type(await screen.findByLabelText('Key'), 'cpu');
    await user.click(screen.getByRole('link', { name: 'Cancel' }));
    const dialog = await screen.findByRole('dialog', { name: 'Are you sure?' });
    expect(dialog).toHaveTextContent('Leave this page?');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await settled();
    expect(router.state.location.pathname).toBe(`${LIST}/new`);
  });
});

describe('SharedCheckFormPage — edit', () => {
  it('loads the definition, puts the change and goes back to the list', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [checkDef()] },
      'PUT /api/v1/admin/checks/definitions/c1': (req: MockRequest) => {
        put = req.body;
        return { definition: checkDef({ interval_seconds: 600 }) };
      },
    });
    const router = renderPage(`${LIST}/c1`);
    expect(await screen.findByRole('heading', { name: 'disk-data', level: 1 })).toBeInTheDocument();
    const interval = screen.getByLabelText('Interval (seconds)');
    await user.clear(interval);
    await user.type(interval, '600');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));

    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
    expect(put).toEqual({ key: 'disk-data', plugin: 'disk', interval_seconds: 600, params: {} });
    expect(await screen.findByText('Check definition disk saved')).toBeInTheDocument();
    expect(document.querySelector('dialog[open]')).toBeNull();
  });

  it('leaves the stored disk mounts intact when the interval is edited', async () => {
    const user = userEvent.setup();
    let put: unknown;
    const withMounts = checkDef({ params: { mounts: ['/data', '/var/lib/postgres'] } });
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [withMounts] },
      'PUT /api/v1/admin/checks/definitions/c1': (req: MockRequest) => {
        put = req.body;
        return { definition: { ...withMounts, interval_seconds: 600 } };
      },
    });
    renderPage(`${LIST}/c1`);
    const interval = await screen.findByLabelText('Interval (seconds)');
    await user.clear(interval);
    await user.type(interval, '600');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put).toMatchObject({ interval_seconds: 600, params: { mounts: ['/data', '/var/lib/postgres'] } });
  });

  it("edits a shared disk check definition's mounts, and an empty list means every mount", async () => {
    const user = userEvent.setup();
    const puts: unknown[] = [];
    const disk = { id: 'cd1', key: 'disk', plugin: 'disk', interval_seconds: 60, params: { mounts: ['/', '/var'] } };
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [disk] },
      'PUT /api/v1/admin/checks/definitions/cd1': (req: MockRequest) => {
        puts.push(req.body);
        return { definition: disk };
      },
    });
    const first = renderPage(`${LIST}/cd1`);
    const mounts = await screen.findByLabelText('Mounts (one per line, empty means every mount)');
    expect(mounts).toHaveValue('/\n/var');
    await user.clear(mounts);
    await user.type(mounts, '/srv');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));
    await waitFor(() => expect(first.state.location.pathname).toBe(LIST));
    expect(puts[0]).toMatchObject({ plugin: 'disk', params: { mounts: ['/srv'] } });
  });

  it('sends no mounts at all once the list is emptied', async () => {
    const user = userEvent.setup();
    let put: unknown;
    const disk = { id: 'cd1', key: 'disk', plugin: 'disk', interval_seconds: 60, params: { mounts: ['/', '/var'] } };
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [disk] },
      'PUT /api/v1/admin/checks/definitions/cd1': (req: MockRequest) => {
        put = req.body;
        return { definition: disk };
      },
    });
    renderPage(`${LIST}/cd1`);
    await user.clear(await screen.findByLabelText('Mounts (one per line, empty means every mount)'));
    await user.click(screen.getByRole('button', { name: 'Save definition' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put).toMatchObject({ plugin: 'disk', params: {} });
  });

  it('shows the not-found page for an id that does not exist', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': { definitions: [checkDef()] },
    });
    renderPage(`${LIST}/nope`);
    expect(await screen.findByText('Not found')).toBeInTheDocument();
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/checks/definitions': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
    });
    renderPage(`${LIST}/c1`);
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});

describe('SharedCheckFormPage — create gate', () => {
  // The create page needs no definitions list: it only checks that the
  // account may create one, from /auth/me, so a failing list cannot block it.
  it('shows the empty form without requesting the definitions list', async () => {
    let listed = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/checks/definitions': () => {
        listed += 1;
        return new Response(JSON.stringify({ detail: 'boom' }), { status: 500 });
      },
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByRole('heading', { name: 'New check definition', level: 1 })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create definition' })).toBeInTheDocument();
    expect(listed).toBe(0);
  });

  it('shows the access-denied page to a non-admin without requesting the definitions list', async () => {
    let listed = 0;
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/checks/definitions': () => {
        listed += 1;
        return { definitions: [] };
      },
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Create definition' })).not.toBeInTheDocument();
    expect(listed).toBe(0);
  });

  describe('network and uptime plugins', () => {
    function mockCreate(): () => unknown {
      let posted: unknown;
      mockApi({
        'GET /api/v1/auth/me': ADMIN,
        'GET /api/v1/admin/checks/definitions': { definitions: [] },
        'POST /api/v1/admin/checks/definitions': (req: MockRequest) => {
          posted = req.body;
          return { definition: checkDef({ id: 'c9', ...(req.body as Record<string, unknown>) }) };
        },
      });
      return () => posted;
    }

    it('sends the interfaces list for network, and {} when it is empty', async () => {
      const user = userEvent.setup();
      const posted = mockCreate();
      renderPage(`${LIST}/new`);
      await screen.findByLabelText('Key');
      await pickOption(screen, 'Plugin', { value: 'network' });
      expect(screen.getByText(/Loopback and container or virtual interfaces are skipped/)).toBeInTheDocument();
      await user.type(screen.getByLabelText(/^Interfaces \(one per line/), 'eth0\n\nbond0\n');
      await user.click(screen.getByRole('button', { name: 'Create definition' }));
      await waitFor(() => expect(posted()).toMatchObject({ plugin: 'network', params: { interfaces: ['eth0', 'bond0'] } }));
    });

    it('sends {} for network without interfaces', async () => {
      const user = userEvent.setup();
      const posted = mockCreate();
      renderPage(`${LIST}/new`);
      await screen.findByLabelText('Key');
      await pickOption(screen, 'Plugin', { value: 'network' });
      await user.click(screen.getByRole('button', { name: 'Create definition' }));
      await waitFor(() => expect(posted()).toMatchObject({ plugin: 'network', params: {} }));
    });

    it("prefills an existing network definition's interfaces on edit and saves them unchanged", async () => {
      const user = userEvent.setup();
      let put: unknown;
      const network = checkDef({ key: 'network-uplinks', plugin: 'network', params: { interfaces: ['eth0', 'bond0'] }, interval_seconds: 60 });
      mockApi({
        'GET /api/v1/auth/me': ADMIN,
        'GET /api/v1/admin/checks/definitions': { definitions: [network] },
        'PUT /api/v1/admin/checks/definitions/c1': (req: MockRequest) => {
          put = req.body;
          return { definition: network };
        },
      });
      renderPage(`${LIST}/c1`);
      expect(await screen.findByLabelText(/^Interfaces \(one per line/)).toHaveValue('eth0\nbond0');
      await user.click(screen.getByRole('button', { name: 'Save definition' }));
      await waitFor(() => expect(put).toBeDefined());
      expect(put).toEqual({ key: 'network-uplinks', plugin: 'network', interval_seconds: 60, params: { interfaces: ['eth0', 'bond0'] } });
    });

    it('sends no parameters for uptime', async () => {
      const user = userEvent.setup();
      const posted = mockCreate();
      renderPage(`${LIST}/new`);
      await screen.findByLabelText('Key');
      await pickOption(screen, 'Plugin', { value: 'uptime' });
      expect(screen.queryByLabelText(/^Interfaces/)).not.toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: 'Create definition' }));
      await waitFor(() => expect(posted()).toMatchObject({ plugin: 'uptime', params: {} }));
    });
  });
});
