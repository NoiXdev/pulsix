import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import type { Locale } from '../../i18n/types.ts';
import type { MockRoute } from '../../test/mockApi.ts';
import { NewPlaybookPage } from './NewPlaybookPage.tsx';
import { PlaybookPage } from './PlaybookPage.tsx';
import { validatePlaybookSearch } from './playbookTabs.ts';

/* The playbook pages' test harness, shared by the PlaybookPage.*.test.tsx
 * and NewPlaybookPage.test.tsx files: an editor playbook `p1` used by one
 * template, a repository playbook `p2` nobody uses, and a router with the
 * list, the new page, the playbook page (which reads `?tab=`) and stand-ins
 * for the pages they lead to. */

export const INLINE = '- name: Install nginx\n  hosts: web\n  tasks: []\n';

export const editorPlaybook = (extra = {}) => ({
  id: 'p1', source: 'editor', summary: 'Install nginx', inline: INLINE, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-18T08:00:00Z', ...extra,
});

export const repoPlaybook = (extra = {}) => ({
  id: 'p2', source: 'repo', summary: '', repo_id: 'r1', path: 'playbooks/site.yml', created_at: '', updated_at: '2026-11-18T08:00:00Z', ...extra,
});

export const REPO = { id: 'r1', name: 'infra', url: 'https://git.example/acme/infra.git', branch: 'main', subpath: '', created_at: '', updated_at: '' };

export const template = (id: string, name: string, playbook_id: string, extra = {}) => ({
  id, name, playbook_id, scope: { group_id: '', host_ids: [] }, limit: '', follow_ups: [], revision: 1, created_at: '', updated_at: '', ...extra,
});

export const TEMPLATES = { templates: [template('t1', 'Deploy web', 'p1', { limit: 'web' }), template('t9', 'Other', 'p9')] };

export const job = (id: string, extra = {}) => ({
  id, kind: 'run', status: 'succeeded', trigger: 'manual', template_id: 't1', run_group_id: id, zone_id: 'z1', created_at: '2026-11-18T09:00:00Z', ...extra,
});

const org = (role: string) => ({ org: { id: 'o1', name: 'Acme', slug: 'acme', role, created_at: '2026-01-01T00:00:00Z' } });

export const PREFS = { density: 'standard', page_size: 25, lists: {} };

export const routes = (role = 'operator', extra: Record<string, MockRoute> = {}): Record<string, MockRoute> => ({
  'GET /api/v1/auth/me/preferences': PREFS,
  'GET /api/v1/orgs/acme': org(role),
  'GET /api/v1/orgs/acme/playbooks': { playbooks: [editorPlaybook(), repoPlaybook()] },
  'GET /api/v1/orgs/acme/playbooks/p1': { playbook: editorPlaybook() },
  'GET /api/v1/orgs/acme/playbooks/p2': { playbook: repoPlaybook() },
  'GET /api/v1/orgs/acme/repos': { repos: [REPO] },
  'GET /api/v1/orgs/acme/job-templates': TEMPLATES,
  'GET /api/v1/orgs/acme/jobs': { jobs: [job('j1')], next_cursor: '', total: 1 },
  'GET /api/v1/orgs/acme/schedules': { schedules: [] },
  'GET /api/v1/orgs/acme/triggers': { triggers: [] },
  ...extra,
});

export function renderAt(path: string, locale?: Locale) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const list = createRoute({ getParentRoute: () => orgRoute, path: '/playbooks', component: () => <div>PLAYBOOKS</div> });
  const create = createRoute({ getParentRoute: () => orgRoute, path: '/playbooks/new', component: NewPlaybookPage });
  const detail = createRoute({ getParentRoute: () => orgRoute, path: '/playbooks/$id', validateSearch: validatePlaybookSearch, component: PlaybookPage });
  const templates = createRoute({ getParentRoute: () => orgRoute, path: '/templates', component: () => <div>TEMPLATES</div> });
  const repos = createRoute({ getParentRoute: () => orgRoute, path: '/repos', component: () => <div>REPOS</div> });
  const run = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([list, create, detail, templates, repos, run])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  const page = (
    <ToastProvider>
      <ConfirmProvider>
        <RouterProvider router={router} />
      </ConfirmProvider>
    </ToastProvider>
  );
  const view = render(<QueryClientProvider client={qc}>{locale ? <I18nProvider locale={locale}>{page}</I18nProvider> : page}</QueryClientProvider>);
  return { ...view, router, qc };
}
