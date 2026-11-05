import type { MockRoute } from '../../../test/mockApi.ts';
import { renderRoutes } from '../../../test/renderRoutes.tsx';
import { TemplateVariablesPage } from './TemplateVariablesPage.tsx';

/** Fixtures of the variables page tests (TemplateVariablesPage*.test.tsx):
 * a linked template at revision 3, its variables and source as the API
 * reads them, a preview, the routes the page reads, and the page in a
 * router beside the template list. Test-only. */

export const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
export const SYNCED_AT = '2026-11-05T10:00:00.123456Z';

export const source = (over: Record<string, unknown> = {}) => ({
  kind: 'repo', commit: '9f8e7d6c5b4a3210', synced_at: SYNCED_AT, applied_rev: '9f8e7d6c5b4a3210', newer: false,
  worker_reads: true, incomplete: false, found: true, left_out: false, available: true, repo_id: 'r1', path: 'site.yml', ...over,
});
export const fields = [
  { name: 'region', type: 'string', required: true, origin: 'source', source: { kind: 'vars_prompt', file: 'site.yml', line: 4 } },
  { name: 'token', type: 'secret', required: true },
  { name: 'debug', type: 'bool', active: false },
];
export const variables = (over: Record<string, unknown> = {}) => ({
  extra_vars_schema: { fields },
  extra_vars: { region: 'eu', token: { $secret: 'set' }, debug: true },
  revision: 3,
  mode: 'manual',
  source: source(),
  review: { gone: 0, needs_review: 0, missing_required: 0, left_out: 0 },
  ...over,
});
export const template = { id: 't1', name: 'deploy-site', playbook_id: 'pb1', scope: { group_id: null, host_ids: [] }, limit: '', extra_vars: {}, revision: 3, vars_mode: 'manual', vault_identities: null, created_at: '', updated_at: '' };

export const preview = {
  source: source({ newer: true, commit: 'aaaaaaa1111111' }),
  preview: {
    source_rev: 'aaaaaaa1111111',
    added: [{ field: { name: 'port', type: 'int', origin: 'source', active: false } }],
    changed: [
      {
        name: 'region', current_origin: 'source', needs_review: false,
        field: { name: 'region', type: 'string', label: 'Region' },
        changes: [{ attr: 'label', old: undefined, new: 'Region' }],
      },
    ],
    adjusted: [],
    gone: [{ name: 'legacy', current_origin: 'source', field: { name: 'legacy', type: 'string', origin: 'gone' } }],
    unchanged: 2,
    left_out: 0,
    notes: [],
  },
};

export function routes(extra: Record<string, MockRoute> = {}): Record<string, MockRoute> {
  return {
    'GET /api/v1/orgs/acme': ORG,
    'GET /api/v1/orgs/acme/job-templates/t1': { template },
    'GET /api/v1/orgs/acme/job-templates/t1/variables': variables(),
    'GET /api/v1/orgs/acme/schedules': { schedules: [] },
    'GET /api/v1/orgs/acme/triggers': { triggers: [] },
    ...extra,
  };
}

export function renderPage() {
  return renderRoutes(
    [
      { path: '/templates/$id/variables', component: TemplateVariablesPage },
      { path: '/templates', component: () => <div>TEMPLATE LIST</div> },
    ],
    '/o/acme/templates/t1/variables',
  );
}

export const status = (code: number, body: unknown = { detail: 'no' }) =>
  new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } });
