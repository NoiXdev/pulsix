import { describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { fetchMock } from '../../test/fetchMock.ts';
import { alertBody, HOST, hostRoutes, orgBody, renderAt, type HostFixture } from '../../test/hostDetail.tsx';
import type { Host } from '../../api/types.ts';
import { HostSidebar } from './HostSidebar.tsx';
import { useHostActions } from './useHostActions.tsx';

function Harness({ host, role = 'admin' }: { host: Host; role?: string }) {
  const actions = useHostActions('acme', host, role);
  return (
    <>
      <HostSidebar org="acme" host={host} role={role} actions={actions} />
      {actions.dialogs}
    </>
  );
}

function renderSidebar(host: Partial<HostFixture> = {}) {
  return renderAt(() => <Harness host={{ ...HOST, ...host } as Host} />);
}

/** The sidebar's facts block (a <dl>). */
const facts = () => within(screen.getByTestId('host-facts'));

describe('HostSidebar', () => {
  it('shows name, facts and capability switches', async () => {
    mockApi(
      hostRoutes(
        { host: { managed: true, monitored: true } },
        {
          'GET /api/v1/orgs/acme/hosts/h1/agent': {
            agent: { agent_name: 'a', host_id: 'h1', host_name: 'web-04.fra', status: 'online', version: '1.0.0', version_status: 'current', created_at: '' },
          },
          'GET /api/v1/orgs/acme/bastions': { bastions: [{ id: 'b1', name: 'bastion-fra' }] },
          'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': {
            vars: {},
            connection: { host_id: 'h1', bastion_source: 'zone', bastions: ['b1'] },
            warnings: [],
          },
        },
      ),
    );
    renderSidebar({ managed: true, monitored: true });

    expect(await screen.findByRole('heading', { level: 1, name: 'web-04.fra' })).toBeInTheDocument();
    expect(await screen.findByText('fra-1')).toBeInTheDocument();
    expect(facts().getByText('203.0.113.4')).toBeInTheDocument();
    expect(facts().getByText('debian 13')).toBeInTheDocument();
    expect(await facts().findByText('online · 1.0.0')).toBeInTheDocument();
    expect(await facts().findByText('bastion-fra')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Managed' })).toBeChecked();
    expect(screen.getByRole('switch', { name: 'Monitored' })).toBeChecked();
  });

  it('shows no zone until the zones are loaded, not a piece of the zone id', async () => {
    const zoneId = 'f00dcafe-0000-4000-8000-000000000001';
    mockApi(hostRoutes({ host: { zone_id: zoneId } }));
    // The zones request never answers: the sidebar must not fill the gap
    // with the id it has.
    const answered = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation((input, init) => {
      const url = input instanceof Request ? input.url : String(input);
      if (new URL(url, 'http://localhost').pathname === '/api/v1/orgs/acme/zones') return new Promise<Response>(() => {});
      return answered(input, init);
    });
    renderSidebar({ zone_id: zoneId });

    await screen.findByRole('heading', { level: 1, name: 'web-04.fra' });
    const zone = facts().getByText('Zone').nextElementSibling!;
    expect(zone).toHaveTextContent('—');
    expect(screen.queryByText(/f00dcafe/)).not.toBeInTheDocument();
  });

  it('reads a direct route, a missing agent and an unknown system plainly', async () => {
    mockApi(hostRoutes({ host: { managed: true, facts: {} } }));
    renderSidebar({ managed: true, facts: {} });

    await screen.findByTestId('host-facts');
    expect(await facts().findByText('direct')).toBeInTheDocument();
    expect(await facts().findByText('no agent')).toBeInTheDocument();
    expect(facts().getAllByText('—')).toHaveLength(1);
  });

  it.each([
    { name: 'no open alerts', alerts: [], want: 'OK' },
    { name: 'two warnings', alerts: [alertBody('a1', 'warn'), alertBody('a2', 'warn')], want: '2 warnings' },
    { name: 'one critical among others', alerts: [alertBody('a1', 'warn'), alertBody('a2', 'crit')], want: '1 critical' },
  ])('state pill: $name', async ({ alerts, want }) => {
    mockApi(hostRoutes({ host: { monitored: true } }, { 'GET /api/v1/orgs/acme/alerts': { alerts, next_cursor: '' } }));
    renderSidebar({ monitored: true });

    expect(await screen.findByText(want)).toBeInTheDocument();
  });

  it('state pill reads "Monitoring off" when the host is not monitored, open alerts or not', async () => {
    mockApi(hostRoutes({}, { 'GET /api/v1/orgs/acme/alerts': { alerts: [alertBody('a1', 'crit')], next_cursor: '' } }));
    renderSidebar();

    expect(await screen.findByText('Monitoring off')).toBeInTheDocument();
    expect(screen.queryByText('1 critical')).not.toBeInTheDocument();
  });

  it('toggles the Managed capability', async () => {
    const user = userEvent.setup();
    let patched: unknown;
    mockApi(
      hostRoutes(
        {},
        {
          'PATCH /api/v1/orgs/acme/hosts/h1/capabilities': (req: MockRequest) => {
            patched = req.body;
            return { host: { ...HOST, managed: true } };
          },
        },
      ),
    );
    renderSidebar();

    await user.click(await screen.findByRole('switch', { name: 'Managed' }));
    await waitFor(() => expect(patched).toEqual({ managed: true }));
  });

  it('adds and removes a manual tag', async () => {
    const user = userEvent.setup();
    const puts: unknown[] = [];
    mockApi(
      hostRoutes(
        {},
        {
          'GET /api/v1/orgs/acme/hosts/h1/tags': { tags: [{ key: 'env', value: 'prod', source: 'manual' }] },
          'PUT /api/v1/orgs/acme/hosts/h1/tags': (req: MockRequest) => {
            puts.push(req.body);
            return { tags: [], host_revision: 4 };
          },
        },
      ),
    );
    renderSidebar();

    await user.type(await screen.findByPlaceholderText('role=db'), 'role=db');
    await user.click(screen.getByRole('button', { name: 'Add tag' }));
    await waitFor(() => expect(puts[0]).toEqual([{ key: 'env', value: 'prod' }, { key: 'role', value: 'db' }]));

    await user.click(screen.getByRole('button', { name: 'remove env' }));
    await waitFor(() => expect(puts[1]).toEqual([]));
  });

  it('sends only the manual tags when a tag is added or removed', async () => {
    const user = userEvent.setup();
    const puts: unknown[] = [];
    mockApi(
      hostRoutes(
        {},
        {
          'GET /api/v1/orgs/acme/hosts/h1/tags': {
            tags: [
              { key: 'env', value: 'prod', source: 'manual' },
              { key: 'os', value: 'debian', source: 'fact' },
            ],
          },
          'PUT /api/v1/orgs/acme/hosts/h1/tags': (req: MockRequest) => {
            puts.push(req.body);
            return { tags: [], host_revision: 4 };
          },
        },
      ),
    );
    renderSidebar();

    await user.type(await screen.findByPlaceholderText('role=db'), 'team=ops');
    await user.click(screen.getByRole('button', { name: 'Add tag' }));
    await waitFor(() => expect(puts[0]).toEqual([{ key: 'env', value: 'prod' }, { key: 'team', value: 'ops' }]));

    await user.click(screen.getByRole('button', { name: 'remove env' }));
    await waitFor(() => expect(puts[1]).toEqual([]));
  });

  it("sends the host's revision as If-Match with a tag change", async () => {
    const user = userEvent.setup();
    let ifMatch: string | null = null;
    mockApi(
      hostRoutes(
        {},
        {
          'PUT /api/v1/orgs/acme/hosts/h1/tags': (req: MockRequest) => {
            ifMatch = req.headers.get('If-Match');
            return { tags: [{ key: 'role', value: 'db', source: 'manual' }], host_revision: 4 };
          },
        },
      ),
    );
    renderSidebar();

    await user.type(await screen.findByPlaceholderText('role=db'), 'role=db');
    await user.click(screen.getByRole('button', { name: 'Add tag' }));
    await waitFor(() => expect(ifMatch).toBe('"3"'));
  });

  it('a 412 on a tag change offers Reload, which refetches the host and its tags', async () => {
    const user = userEvent.setup();
    let hostGets = 0;
    let tagGets = 0;
    mockApi(
      hostRoutes(
        {},
        {
          'GET /api/v1/orgs/acme/hosts/h1': () => {
            hostGets += 1;
            return { host: { ...HOST, revision: 9 } };
          },
          'GET /api/v1/orgs/acme/hosts/h1/tags': () => {
            tagGets += 1;
            return { tags: [] };
          },
          'PUT /api/v1/orgs/acme/hosts/h1/tags': () =>
            new Response(JSON.stringify({ title: 'x', status: 412, detail: 'the host changed since you loaded it; reload and try again' }), {
              status: 412,
              headers: { 'Content-Type': 'application/problem+json' },
            }),
        },
      ),
    );
    renderSidebar();

    const field = await screen.findByPlaceholderText('role=db');
    await user.type(field, 'role=db');
    await user.click(screen.getByRole('button', { name: 'Add tag' }));
    expect(await screen.findByText('This host was changed meanwhile — reload it and apply your change again')).toBeInTheDocument();
    expect(field).toHaveValue('role=db');
    const hostsBefore = hostGets;
    const tagsBefore = tagGets;

    await user.click(screen.getByRole('button', { name: 'Reload' }));

    await waitFor(() => expect(screen.queryByText(/changed meanwhile/)).not.toBeInTheDocument());
    expect(hostGets).toBeGreaterThan(hostsBefore);
    expect(tagGets).toBeGreaterThan(tagsBefore);
  });

  it('keeps a manual tag and a fact tag of the same key apart', async () => {
    const user = userEvent.setup();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    try {
      const puts: unknown[] = [];
      const fact = { key: 'os', value: 'debian', source: 'fact' };
      const manual = { key: 'os', value: 'ubuntu', source: 'manual' };
      mockApi(
        hostRoutes(
          {},
          {
            'GET /api/v1/orgs/acme/hosts/h1/tags': () => ({ tags: puts.length ? [fact, manual] : [fact] }),
            'PUT /api/v1/orgs/acme/hosts/h1/tags': (req: MockRequest) => {
              puts.push(req.body);
              return { tags: [fact, manual], host_revision: 4 };
            },
          },
        ),
      );
      renderSidebar();

      expect(await screen.findByText('os=debian')).toBeInTheDocument();
      await user.type(screen.getByPlaceholderText('role=db'), 'os=ubuntu');
      await user.click(screen.getByRole('button', { name: 'Add tag' }));

      await waitFor(() => expect(puts[0]).toEqual([{ key: 'os', value: 'ubuntu' }]));
      expect(await screen.findByText('os=ubuntu')).toBeInTheDocument();
      expect(screen.getByText('os=debian')).toBeInTheDocument();
      // Two chips of one key must not share a React key.
      const warnings = consoleError.mock.calls.filter((call) => String(call[0]).includes('same key'));
      expect(warnings).toEqual([]);
    } finally {
      consoleError.mockRestore();
    }
  });

  it('rejects a malformed tag chip inline without a round trip', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes());
    renderSidebar();

    await user.type(await screen.findByPlaceholderText('role=db'), 'Role=db');
    await user.click(screen.getByRole('button', { name: 'Add tag' }));

    expect(await screen.findByText(/not a valid tag key/)).toBeInTheDocument();
  });

  it('offers Run command, Edit and a menu with Bootstrap and Delete to an admin on a managed host', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes({ host: { managed: true } }));
    renderSidebar({ managed: true });

    expect(await screen.findByRole('button', { name: 'Run command' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run template…' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'More actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Bootstrap…', 'Delete…']);

    await user.click(screen.getByRole('menuitem', { name: 'Bootstrap…' }));
    expect(await screen.findByRole('dialog', { name: 'Bootstrap web-04.fra' })).toBeInTheDocument();
  });

  it('opens the Run template dialog from its button', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes({ host: { managed: true } }, { 'GET /api/v1/orgs/acme/job-templates': { templates: [] } }));
    renderSidebar({ managed: true });

    await user.click(await screen.findByRole('button', { name: 'Run template…' }));
    expect(await screen.findByRole('dialog', { name: 'Run template on web-04.fra' })).toBeInTheDocument();
  });

  it('offers no Bootstrap and no Run command for a host that is not managed', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes());
    renderSidebar();

    await user.click(await screen.findByRole('button', { name: 'More actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Delete…']);
    expect(screen.queryByRole('button', { name: 'Run command' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Run template…' })).not.toBeInTheDocument();
  });

  it('gives a viewer read-only switches and no actions', async () => {
    mockApi(hostRoutes({ role: 'viewer', host: { managed: true } }));
    renderAt(() => <Harness host={{ ...HOST, managed: true } as Host} role="viewer" />);

    expect(await screen.findByText('fra-1')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Managed' })).toBeDisabled();
    expect(screen.getByRole('switch', { name: 'Monitored' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Run command' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Run template…' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /More actions/ })).not.toBeInTheDocument();
    expect(screen.queryByPlaceholderText('role=db')).not.toBeInTheDocument();
  });

  it('takes the role from its caller, and so do its actions: no request for the organization', async () => {
    const asked: string[] = [];
    mockApi(
      hostRoutes(
        { role: 'viewer', host: { managed: true } },
        {
          'GET /api/v1/orgs/acme': (req: MockRequest) => {
            asked.push(req.path);
            return orgBody('viewer');
          },
        },
      ),
    );
    renderAt(() => <Harness host={{ ...HOST, managed: true } as Host} role="viewer" />);

    expect(await screen.findByText('fra-1')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Managed' })).toBeDisabled();
    expect(screen.getByRole('switch', { name: 'Monitored' })).toBeDisabled();
    expect(screen.queryByPlaceholderText('role=db')).not.toBeInTheDocument();
    expect(asked).toEqual([]);
  });
});
