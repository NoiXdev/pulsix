import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { RulesPage } from './RulesPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

const rule = (over: Record<string, unknown> = {}) => ({
  id: 'r1',
  name: 'CPU usage warning',
  enabled: true,
  origin: 'seed',
  scope: 'org',
  check_key: 'cpu',
  metric: 'cpu.used_pct',
  severity: 'warn',
  mode: 'form',
  form: { metric: 'cpu.used_pct', op: '>=', value: 90 },
  source: 'cpu.used_pct >= 90',
  for_seconds: 0,
  created_at: '',
  updated_at: '',
  ...over,
});

const METRICS = {
  metrics: [
    { name: 'cpu.used_pct', check_key: 'cpu', kind: 'numeric' },
    { name: 'mem.used_pct', check_key: 'memory', kind: 'numeric' },
    { name: 'service.state', check_key: 'service', kind: 'discrete' },
  ],
};

const GROUPS = { groups: [{ id: 'g1', name: 'Web tier', selector: 'role=web', priority: 10, created_at: '', updated_at: '' }] };

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks' });
  const checksIndexRoute = createRoute({ getParentRoute: () => checksRoute, path: '/', component: () => <div>CHECKS VIEW</div> });
  const groupsRoute = createRoute({ getParentRoute: () => checksRoute, path: '/monitoring-groups', component: () => <div>GROUPS VIEW</div> });
  const rulesRoute = createRoute({ getParentRoute: () => checksRoute, path: '/rules', component: RulesPage });
  const hostRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts/$id', component: () => <div>HOST VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      orgRoute.addChildren([checksRoute.addChildren([checksIndexRoute, groupsRoute, rulesRoute]), hostRoute]),
    ]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/checks/rules'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('RulesPage', () => {
  it('lists rules grouped by metric, so two rules on the same metric sit together', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': {
        rules: [
          rule(),
          rule({
            id: 'r2', name: 'CPU usage critical', severity: 'crit', for_seconds: 300,
            form: { metric: 'cpu.used_pct', op: '>=', value: 97 },
          }),
          rule({
            id: 'r3', name: 'Web tier memory', origin: 'user', scope: 'group', group_id: 'g1', metric: 'mem.used_pct', check_key: 'memory',
            form: { metric: 'mem.used_pct', op: '>=', value: 85 },
          }),
        ],
      },
    });
    renderPage();

    // Both cpu.used_pct rules sit under the same group heading.
    const cpuHeading = await screen.findByText('cpu.used_pct');
    const memHeading = screen.getByText('mem.used_pct');
    expect(cpuHeading).toBeInTheDocument();
    expect(memHeading).toBeInTheDocument();
    expect(screen.getByText('CPU usage warning')).toBeInTheDocument();
    expect(screen.getByText('CPU usage critical')).toBeInTheDocument();
    expect(screen.getByText('Web tier memory')).toBeInTheDocument();

    // Severity, scope target, duration and origin are all on the row.
    expect(screen.getAllByText('warn')).toHaveLength(2);
    expect(screen.getByText('crit')).toBeInTheDocument();
    expect(screen.getAllByText('seed')).toHaveLength(2);
    expect(screen.getByText('user')).toBeInTheDocument();
    expect(screen.getAllByText('Organization')).toHaveLength(2);
    expect(screen.getByRole('link', { name: 'Web tier' })).toBeInTheDocument();
    expect(screen.getAllByText('0 s')).toHaveLength(2);
    expect(screen.getByText('5 min')).toBeInTheDocument();
  });

  // backlog 3.30: RuleDTO now carries host_name/group_name directly, so a
  // host- or group-scoped rule's "Applies to" column shows a name even
  // when it is not also present on the organization's own, separately
  // loaded monitoring-group list — before this, a host target always fell
  // back to its shortened id (RuleDTO carried no host_name at all), and a
  // group target depended entirely on the groups list already containing
  // a match.
  it('names the host and group a rule targets, from the rule itself', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': {
        rules: [
          rule({
            id: 'r-host', name: 'Host memory', scope: 'host', host_id: 'h1', host_name: 'web-1',
            metric: 'mem.used_pct', check_key: 'memory', form: { metric: 'mem.used_pct', op: '>=', value: 90 },
          }),
          rule({
            id: 'r-group', name: 'Group disk', scope: 'group', group_id: 'g-unlisted', group_name: 'Db tier',
            metric: 'disk.used_pct', check_key: 'disk', form: { metric: 'disk.used_pct', op: '>=', value: 90 },
          }),
        ],
      },
    });
    renderPage();

    expect(await screen.findByRole('link', { name: 'web-1' })).toBeInTheDocument();
    // "g-unlisted" is deliberately absent from GROUPS, so this name can
    // only have come from the rule's own group_name, not a cross-reference
    // into the groups list.
    expect(screen.getByRole('link', { name: 'Db tier' })).toBeInTheDocument();
  });

  it('shows a viewer the rules but no write affordances', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': { rules: [rule()] },
    });
    renderPage();

    expect(await screen.findByText('CPU usage warning')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New rule' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit CPU usage warning' })).not.toBeInTheDocument();
  });

  it('creates a form rule with the body the API documents', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': { rules: [] },
      'POST /api/v1/orgs/acme/rules': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { rule: rule({ id: 'r9', origin: 'user' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New rule' }));
    await user.type(screen.getByLabelText('Name'), 'High CPU');
    await pickOption(screen, 'Metric', { value: 'cpu.used_pct' });
    await pickOption(screen, 'Operator', { value: '>' });
    await user.type(screen.getByLabelText('Value'), '90');
    // A numeric metric's optional clear condition, for the hysteresis pair
    // internal/rules' Form.Compile builds.
    await pickOption(screen, 'Clear condition', { value: '<' });
    await user.type(screen.getByLabelText('Clear value'), '80');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toEqual({
        name: 'High CPU',
        enabled: true,
        scope: 'org',
        metric: 'cpu.used_pct',
        severity: 'warn',
        for_seconds: 0,
        mode: 'form',
        form: { metric: 'cpu.used_pct', op: '>', value: 90, clear_op: '<', clear_value: 80 },
      }),
    );
  });

  it('creates a rule on a discrete metric with a single-label operator', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': { rules: [] },
      'POST /api/v1/orgs/acme/rules': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { rule: rule({ id: 'r9', origin: 'user', metric: 'service.state' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New rule' }));
    await user.type(screen.getByLabelText('Name'), 'Service down');
    await pickOption(screen, 'Metric', { value: 'service.state' });
    await pickOption(screen, 'Operator', { value: '!=' });
    await user.type(screen.getByLabelText('Label'), 'running');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toMatchObject({ metric: 'service.state', mode: 'form', form: { metric: 'service.state', op: '!=', labels: ['running'] } }),
    );
    expect(posted).not.toHaveProperty('form.value');
  });

  it('creates a rule on a discrete metric with the "in" operator and several labels', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': { rules: [] },
      'POST /api/v1/orgs/acme/rules': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { rule: rule({ id: 'r9', origin: 'user', metric: 'service.state' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New rule' }));
    await user.type(screen.getByLabelText('Name'), 'Service unhealthy');
    await pickOption(screen, 'Metric', { value: 'service.state' });
    await pickOption(screen, 'Operator', { value: 'in' });
    await user.type(screen.getByLabelText('Labels (one per line)'), 'failed\ndead');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toMatchObject({ form: { metric: 'service.state', op: 'in', labels: ['failed', 'dead'] } }),
    );
  });

  it('binds a rule to a single monitored host', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': { rules: [] },
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'web-1' }], next_cursor: '' },
      'POST /api/v1/orgs/acme/rules': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { rule: rule({ id: 'r9', origin: 'user', scope: 'host', host_id: 'h1' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New rule' }));
    await user.type(screen.getByLabelText('Name'), 'Host CPU');
    await pickOption(screen, 'Applies to', { value: 'host' });
    await pickOption(screen, 'Host', { value: 'h1' });
    await user.type(screen.getByLabelText('Value'), '90');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(posted).toMatchObject({ scope: 'host', host_id: 'h1' }));
    expect(posted).not.toHaveProperty('group_id');
  });

  it('deletes a rule once the operator has confirmed it', async () => {
    const user = userEvent.setup();
    const confirmed = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': { rules: [rule({ origin: 'user' })] },
      'DELETE /api/v1/orgs/acme/rules/r1': () => {
        deleted += 1;
        return {};
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Delete CPU usage warning' }));
    await waitFor(() => expect(deleted).toBe(1));
    expect(confirmed.mock.calls[0][0]).toContain('CPU usage warning');
    confirmed.mockRestore();
  });

  it('keeps the section tabs when the rules list fails', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/rules/metrics': METRICS,
      'GET /api/v1/orgs/acme/rules': () => new Response('{"detail":"boom"}', { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Monitoring groups' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Rules' })).toBeInTheDocument();
  });
});
