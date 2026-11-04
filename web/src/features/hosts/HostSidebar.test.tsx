import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { QueryClient } from '@tanstack/react-query';
import { alertBody, HOST, hostRoutes, orgBody, renderAt, type HostFixture } from '../../test/hostDetail.tsx';
import type { Host } from '../../api/types.ts';
import { HostSidebar } from './HostSidebar.tsx';
import { useHostActions } from './useHostActions.tsx';

function Harness({ host }: { host: Host }) {
  const actions = useHostActions('acme', host);
  return (
    <>
      <HostSidebar org="acme" host={host} actions={actions} />
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
            return { tags: [] };
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
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'More actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Bootstrap…', 'Delete…']);

    await user.click(screen.getByRole('menuitem', { name: 'Bootstrap…' }));
    expect(await screen.findByRole('dialog', { name: 'Bootstrap web-04.fra' })).toBeInTheDocument();
  });

  it('offers no Bootstrap and no Run command for a host that is not managed', async () => {
    const user = userEvent.setup();
    mockApi(hostRoutes());
    renderSidebar();

    await user.click(await screen.findByRole('button', { name: 'More actions for web-04.fra' }));
    const items = await screen.findAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(['Delete…']);
    expect(screen.queryByRole('button', { name: 'Run command' })).not.toBeInTheDocument();
  });

  it('gives a viewer read-only switches and no actions', async () => {
    mockApi(hostRoutes({ role: 'viewer', host: { managed: true } }));
    // The role is already known, as it is on the page (which waits for it):
    // otherwise every assertion below would also hold before it landed.
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(['org', 'acme'], orgBody('viewer'));
    renderAt(() => <Harness host={{ ...HOST, managed: true } as Host} />, qc);

    expect(await screen.findByText('fra-1')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: 'Managed' })).toBeDisabled();
    expect(screen.getByRole('switch', { name: 'Monitored' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Run command' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /More actions/ })).not.toBeInTheDocument();
    expect(screen.queryByPlaceholderText('role=db')).not.toBeInTheDocument();
  });
});
