import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider, useParams, useSearch } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import type { MockRoute } from '../../test/mockApi.ts';
import { TemplateDetailPage } from './TemplateDetailPage.tsx';
import { validateTemplateSearch } from './templateTabs.ts';

/** Shared fixtures for the template page's tests. */

export const orgAs = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

export function template(over: Record<string, unknown> = {}) {
  return {
    id: 't1', name: 'deploy-site', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, revision: 1,
    vars_mode: 'off', created_at: '', updated_at: '', follow_ups: [], vault_identities: null, agent_enrollment: false, ...over,
  };
}

export const playbookRow = { id: 'pb1', source: 'repo', repo_id: 'r1', path: 'site.yml', summary: 'site.yml', revision: 1, created_at: '', updated_at: '' };

export const repoRow = {
  id: 'r1', name: 'infra', url: 'git@example.com:infra.git', branch: 'main', subpath: '', created_at: '', updated_at: '', vault_identities: null,
  last_synced_commit: '0123456789abcdef0123', last_synced_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
};

export function job(id: string, over: Record<string, unknown> = {}) {
  return { id, kind: 'run', status: 'succeeded', trigger: 'manual', template_id: 't1', zone_id: 'z1', run_group_id: `g-${id}`, check_mode: false, withdrawn_at_claim: false, created_at: new Date(Date.now() - 60_000).toISOString(), ...over };
}

export function schedule(id: string, over: Record<string, unknown> = {}) {
  return {
    id, name: `nightly-${id}`, template_id: 't1', cron: '0 3 * * *', description: 'every day at 03:00', mode: 'cron', timezone: 'UTC', paused: false,
    guards: {}, revision: 1, created_at: '', updated_at: '', next_run_at: new Date(Date.now() + 2 * 3600_000).toISOString(), ...over,
  };
}

export function trigger(id: string, over: Record<string, unknown> = {}) {
  return {
    id, name: `push-${id}`, kind: 'template_run', template_id: 't1', enabled: true, preset: 'github', auth: 'hmac', allow_cidrs: null, on_active: 'queue',
    signature_header: 'X-Hub-Signature-256', mapping: {}, rate_limit: { count: 10, per: 'minute' }, revision: 1, created_at: '', updated_at: '', url: `https://pulsix.example/hooks/${id}`, ...over,
  };
}

export function runRequest(id: string, over: Record<string, unknown> = {}) {
  return {
    id, kind: 'run', template_id: 't1', template_revision: 1, reason: `Request ${id}`, status: 'pending', requested_by_user_id: 'u1', requested_by_token_name: 'claude-ops',
    created_at: '2026-11-26T08:00:00Z', expires_at: new Date(Date.now() + 30 * 60_000).toISOString(), ...over,
  };
}

/** The template's variables as the variables tab reads them: one field. */
export function variables(over: Record<string, unknown> = {}) {
  return {
    extra_vars_schema: { fields: [{ name: 'region', type: 'string', required: true }] },
    extra_vars: { region: 'eu' },
    revision: 1,
    mode: 'off',
    review: { gone: 0, needs_review: 0, missing_required: 0, left_out: 0, not_sent: [] },
    ...over,
  };
}

/** The routes the page reads; `over` replaces or adds any. */
export function pageRoutes({ role = 'operator', tpl = template(), over = {} }: { role?: string; tpl?: ReturnType<typeof template>; over?: Record<string, MockRoute> } = {}): Record<string, MockRoute> {
  return {
    'GET /api/v1/orgs/acme': orgAs(role),
    'GET /api/v1/orgs/acme/job-templates': { templates: [tpl] },
    'GET /api/v1/orgs/acme/job-templates/t1': { template: tpl },
    'GET /api/v1/orgs/acme/playbooks': { playbooks: [playbookRow] },
    'GET /api/v1/orgs/acme/repos': { repos: [repoRow] },
    'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
    'GET /api/v1/orgs/acme/credentials': { credentials: [] },
    'GET /api/v1/orgs/acme/schedules': { schedules: [] },
    'GET /api/v1/orgs/acme/triggers': { triggers: [] },
    'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '' },
    'GET /api/v1/orgs/acme/run-requests': { requests: [] },
    'GET /api/v1/orgs/acme/job-templates/t1/variables': variables(),
    'GET /api/v1/auth/me/preferences': { density: 'standard', page_size: 25, lists: {} },
    'PUT /api/v1/auth/me/preferences': (req: { body: unknown }) => req.body,
    ...over,
  };
}

/** renderTemplatePage renders the template page at `path` inside the
 * routes it links to (each a stand-in naming itself). */
export function renderTemplatePage(path = '/o/acme/templates/t1') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/templates/$id', validateSearch: validateTemplateSearch, component: TemplateDetailPage });
  // A stand-in for a page the template page leads to: its name, the id in
  // its path and its search.
  const stand = (p: string, name: string) =>
    createRoute({
      getParentRoute: () => orgRoute,
      path: p,
      validateSearch: (s: Record<string, unknown>) => s,
      component: function Landed() {
        const params = useParams({ strict: false }) as { id?: string };
        const search = useSearch({ strict: false });
        return (
          <div>
            {name}
            {params.id ? ` ${params.id}` : ''} {JSON.stringify(search)}
          </div>
        );
      },
    });
  const routeTree = rootRoute.addChildren([
    orgRoute.addChildren([
      page,
      stand('/templates', 'TEMPLATES LIST'),
      stand('/playbooks/$id', 'PLAYBOOK PAGE'),
      stand('/runs', 'RUNS LIST'),
      stand('/runs/$id', 'RUN PAGE'),
      stand('/runs/requests', 'REQUESTS LIST'),
      stand('/runs/requests/$id', 'REQUEST PAGE'),
      stand('/schedules', 'SCHEDULES LIST'),
    ]),
  ]);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  const result = render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return { ...result, router, qc };
}
