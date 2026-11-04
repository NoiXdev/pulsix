import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { de } from '../../i18n/de/index.ts';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { SharedResourcesPage } from './SharedResourcesPage.tsx';

const ADMIN = {
  user: { id: 'u1', email: 'admin@acme.io', display_name: 'Ada' },
  mfa_complete: true,
  mfa_enrolment_required: false,
  totp_confirmed: true,
  instance_admin: true,
};

const def = (over: Record<string, unknown> = {}) => ({
  id: 'd1', name: 'ansible-base', url: 'https://git.example/provider/ansible-base.git', branch: 'main', subpath: '', ...over,
});

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const sharedRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/shared-resources', component: SharedResourcesPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([sharedRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/shared-resources'] }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

// v0.2f Task 5: the instance-admin screen that creates, edits and deletes
// repository definitions (spec decisions 1-8) — the installation-owned side
// of what ReposPage.tsx's RepoDialog shows read-only inside an organization.
describe('SharedResourcesPage', () => {
  it('lists repository definitions and shows the Shared resources title', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
    });
    renderPage();
    expect(await screen.findByText('ansible-base')).toBeInTheDocument();
    expect(screen.getByText('https://git.example/provider/ansible-base.git')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Shared resources', level: 1 })).toBeInTheDocument();
  });

  it('creates a repository definition, projected into every organization by the server', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const definitions: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': () => ({ definitions }),
      'POST /api/v1/admin/repos/definitions': (req: MockRequest) => {
        posted = req.body;
        const created = def({ id: 'd9', ...(req.body as Record<string, unknown>) });
        definitions.push(created);
        return { definition: created };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository definition' }));
    await user.type(screen.getByLabelText('Name'), 'infra-base');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/provider/infra-base.git');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));

    expect(await screen.findByText('infra-base')).toBeInTheDocument();
    expect(posted).toMatchObject({ name: 'infra-base', url: 'https://git.example/provider/infra-base.git' });
    expect(await screen.findByText('Repository definition infra-base created')).toBeInTheDocument();
  });

  it('does not submit while the required fields are empty', async () => {
    const user = userEvent.setup();
    let posts = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'POST /api/v1/admin/repos/definitions': () => {
        posts += 1;
        return { definition: def() };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository definition' }));
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findAllByText('required')).toHaveLength(2);
    expect(posts).toBe(0);
  });

  it('edits a definition, propagating its branch to every projection', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
      'PUT /api/v1/admin/repos/definitions/d1': (req: MockRequest) => {
        put = req.body;
        return { definition: def({ branch: 'release' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'ansible-base' }));
    const dialog = await screen.findByRole('dialog', { name: 'ansible-base' });
    const branch = within(dialog).getByLabelText('Branch');
    await user.clear(branch);
    await user.type(branch, 'release');
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(put).toEqual({
      name: 'ansible-base', url: 'https://git.example/provider/ansible-base.git', branch: 'release', subpath: '',
    });
    expect(await screen.findByText('Repository definition ansible-base saved')).toBeInTheDocument();
  });

  // Spec decision 7: a name collision names the colliding organizations —
  // to an instance admin only, which this screen is. The server composes
  // the full sentence (mapSharedErr, internal/api/shared_handlers.go)
  // specifically so this is the one screen that can show it whole.
  it('shows which organizations already use a name when creating a definition collides', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'POST /api/v1/admin/repos/definitions': () =>
        new Response(JSON.stringify({ detail: 'name "infra" is already used by acme, widget-co' }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository definition' }));
    await user.type(screen.getByLabelText('Name'), 'infra');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/provider/infra.git');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name "infra" is already used by acme, widget-co');
  });

  // Spec decision 5: a definition a customer's schedule, trigger or
  // playbook still references refuses to delete, naming the organization
  // and what still references it.
  it('shows which organization and what blocks a delete', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [def()] },
      'DELETE /api/v1/admin/repos/definitions/d1': () =>
        new Response(
          JSON.stringify({ detail: "organization acme: job triggers still reference this definition's projection" }),
          { status: 409, headers: { 'Content-Type': 'application/json' } },
        ),
    });
    renderPage();
    await screen.findByText('ansible-base');
    await user.click(screen.getByRole('button', { name: 'Delete ansible-base' }));
    expect(await screen.findByText("organization acme: job triggers still reference this definition's projection")).toBeInTheDocument();
    // Refused, not removed: the row is still there.
    expect(screen.getByText('ansible-base')).toBeInTheDocument();
  });

  it('deletes a definition that nothing references', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': () => ({ definitions: deleted ? [] : [def()] }),
      'DELETE /api/v1/admin/repos/definitions/d1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Delete ansible-base' }));
    expect(await screen.findByText('No shared repository definitions yet.')).toBeInTheDocument();
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
      'GET /api/v1/admin/templates/definitions': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
      'GET /api/v1/admin/checks/definitions': () =>
        new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 }),
    });
    renderPage();
    // All three sections are independent queries behind the same gate
    // (Task 6's own doc comment on SharedResourcesPage, extended by Task 7
    // for the third kind), so a non-admin sees the access-denied state from
    // each of them, not merely one or two. The third section's own query
    // (TemplateDefinitionsSection fires two requests — its own and
    // useRepoDefinitions' reused one — ahead of it) settles slightly later
    // than the default 1000ms findBy* timeout under load, so this waits
    // longer rather than racing it.
    await waitFor(() => expect(screen.getAllByText('You do not have access')).toHaveLength(3), { timeout: 3000 });
  });
});

// v0.2f Task 6, ruled in after Task 5 shipped: the instance-admin half of
// job templates was API-only (only TemplateDialog.tsx's read-only marker
// existed). This section is RepoDefinitionsSection's own test block,
// mirrored one-for-one for templates — the extension point Task 5's own
// report named ("the title 'Shared resources' vs. the one table's own
// 'repository definitions' framing already leaves room for that"), which
// held: SharedResourcesPage.tsx needed no restructuring, only a second,
// independent section.
describe('SharedResourcesPage — job template definitions (v0.2f Task 6)', () => {
  const repoDef = (over: Record<string, unknown> = {}) => ({
    id: 'r1', name: 'infra-base', url: 'https://git.example/provider/infra-base.git', branch: 'main', subpath: '', ...over,
  });
  const tplDef = (over: Record<string, unknown> = {}) => ({
    id: 't1', name: 'deploy-site', shared_repo_id: 'r1', playbook_path: 'site.yml', limit: '', extra_vars: {}, ...over,
  });

  it('lists job template definitions, naming the repository they resolve against', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef({ limit: 'web*' })] },
    });
    renderPage();
    expect(await screen.findByText('deploy-site')).toBeInTheDocument();
    // The repository column resolves the id to the OTHER section's own
    // name, not the raw id — proving the two sections, independent as they
    // are, still read from the same repository list. "infra-base" appears
    // twice by design: once as the repo section's own row, once as the
    // template row's resolved repository column.
    expect(await screen.findAllByText('infra-base')).toHaveLength(2);
    expect(screen.getByText('site.yml')).toBeInTheDocument();
    expect(screen.getByText('web*')).toBeInTheDocument();
  });

  it('creates a job template definition, projected into every organization by the server', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const definitions: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': () => ({ definitions }),
      'POST /api/v1/admin/templates/definitions': (req: MockRequest) => {
        posted = req.body;
        const created = tplDef({ id: 't9', ...(req.body as Record<string, unknown>) });
        definitions.push(created);
        return { definition: created };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add job template definition' }));
    await user.type(screen.getByLabelText('Name'), 'deploy-site');
    await pickOption(screen, 'Shared repository', { value: 'r1' });
    await user.type(screen.getByLabelText('Playbook path'), 'site.yml');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));

    expect(await screen.findByText('deploy-site')).toBeInTheDocument();
    expect(posted).toMatchObject({ name: 'deploy-site', shared_repo_id: 'r1', playbook_path: 'site.yml' });
    expect(await screen.findByText('Job template definition deploy-site created')).toBeInTheDocument();
  });

  it('does not submit while the required fields are empty', async () => {
    const user = userEvent.setup();
    let posts = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'POST /api/v1/admin/templates/definitions': () => {
        posts += 1;
        return { definition: tplDef() };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add job template definition' }));
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findAllByText('required')).toHaveLength(3);
    expect(posts).toBe(0);
  });

  it('edits a definition, propagating its limit to every projection', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef()] },
      'PUT /api/v1/admin/templates/definitions/t1': (req: MockRequest) => {
        put = req.body;
        return { definition: tplDef({ limit: 'web*' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.type(within(dialog).getByLabelText('Limit pattern'), 'web*');
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(put).toEqual({
      name: 'deploy-site', shared_repo_id: 'r1', playbook_path: 'site.yml', limit: 'web*', extra_vars: {},
    });
    expect(await screen.findByText('Job template definition deploy-site saved')).toBeInTheDocument();
  });

  // Whole-branch review finding: the PUT this dialog sends is a full
  // replace, and submit() used to build its body from only the fields the
  // form rendered — so editing the limit SILENTLY CLEARED extra vars set
  // through the API, for every organization's projection, in the same
  // transaction. The first fix round-tripped the stored value through
  // submit(); backlog 2.101 then made it editable, so it now round-trips
  // through the form's own state. The property this pins is the same
  // either way, and it is the one that matters: an edit that does not
  // touch extra vars leaves them exactly as they were.
  it('leaves stored extra vars intact when editing another field', async () => {
    const user = userEvent.setup();
    let put: unknown;
    const withExtraVars = tplDef({ extra_vars: { region: 'eu-west-1', replicas: 3 } });
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [withExtraVars] },
      'PUT /api/v1/admin/templates/definitions/t1': (req: MockRequest) => {
        put = req.body;
        return { definition: { ...withExtraVars, limit: 'web*' } };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.type(within(dialog).getByLabelText('Limit pattern'), 'web*');
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(put).toMatchObject({ limit: 'web*', extra_vars: { region: 'eu-west-1', replicas: 3 } });
  });

  // Spec decision 7, Task 6's own application: a name collision names the
  // colliding organizations, to an instance admin only.
  it('shows which organizations already use a name when creating a definition collides', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'POST /api/v1/admin/templates/definitions': () =>
        new Response(JSON.stringify({ detail: 'name "deploy-site" is already used by acme, widget-co' }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add job template definition' }));
    await user.type(screen.getByLabelText('Name'), 'deploy-site');
    await pickOption(screen, 'Shared repository', { value: 'r1' });
    await user.type(screen.getByLabelText('Playbook path'), 'site.yml');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name "deploy-site" is already used by acme, widget-co');
  });

  // Spec decision 5, Task 6's own application: a definition a customer's
  // schedule or job trigger still references refuses to delete, naming the
  // organization and what still references it.
  it('shows which organization and what blocks a delete', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef()] },
      'DELETE /api/v1/admin/templates/definitions/t1': () =>
        new Response(JSON.stringify({ detail: 'organization acme: a schedule still references this definition\'s projection' }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();
    await screen.findByText('deploy-site');
    await user.click(screen.getByRole('button', { name: 'Delete deploy-site' }));
    expect(await screen.findByText("organization acme: a schedule still references this definition's projection")).toBeInTheDocument();
    // Refused, not removed: the row is still there.
    expect(screen.getByText('deploy-site')).toBeInTheDocument();
  });

  it('deletes a definition that nothing references', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': () => ({ definitions: deleted ? [] : [tplDef()] }),
      'DELETE /api/v1/admin/templates/definitions/t1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Delete deploy-site' }));
    expect(await screen.findByText('No shared job template definitions yet.')).toBeInTheDocument();
  });
});

// v0.2f Task 7, the third and last application of RepoDefinitionsSection's
// own shape: check definitions. UNLIKE repositories and job templates, a
// check definition's own delete never refuses with 409 at all (spec
// decision 5 has nothing to block on for this kind — internal/shared's own
// doc comment on DeleteCheckDefinitionWithProjections), so this block has no
// "shows which organization and what blocks a delete" test to mirror; every
// other test mirrors the template block one-for-one.
describe('SharedResourcesPage — check definitions (v0.2f Task 7)', () => {
  const checkDef = (over: Record<string, unknown> = {}) => ({
    id: 'c1', key: 'disk-data', plugin: 'disk', params: {}, interval_seconds: 300, ...over,
  });

  it('lists check definitions', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'GET /api/v1/admin/checks/definitions': { definitions: [checkDef()] },
    });
    renderPage();
    expect(await screen.findByText('disk-data')).toBeInTheDocument();
    expect(screen.getByText('disk')).toBeInTheDocument();
    expect(screen.getByText('300s')).toBeInTheDocument();
  });

  it('creates a check definition, projected into every organization by the server', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const definitions: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'GET /api/v1/admin/checks/definitions': () => ({ definitions }),
      'POST /api/v1/admin/checks/definitions': (req: MockRequest) => {
        posted = req.body;
        const created = checkDef({ id: 'c9', ...(req.body as Record<string, unknown>) });
        definitions.push(created);
        return { definition: created };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add check definition' }));
    await user.type(screen.getByLabelText('Key'), 'cpu-watch');
    await pickOption(screen, 'Plugin', { value: 'memory' });
    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.type(screen.getByLabelText('Interval (seconds)'), '60');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));

    expect(await screen.findByText('cpu-watch')).toBeInTheDocument();
    expect(posted).toMatchObject({ key: 'cpu-watch', plugin: 'memory', interval_seconds: 60 });
    expect(await screen.findByText('Check definition memory created')).toBeInTheDocument();
  });

  it('does not submit while the required fields are empty', async () => {
    const user = userEvent.setup();
    let posts = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
      'POST /api/v1/admin/checks/definitions': () => {
        posts += 1;
        return { definition: checkDef() };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add check definition' }));
    await pickOption(screen, 'Plugin', { value: 'service' });
    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    // Two refusals at once: the cleared interval, and "service" with no
    // units at all — the same shape internal/checks.validateParams enforces
    // server-side, refused client-side before the round trip here too.
    expect(await screen.findAllByText('required')).toHaveLength(2);
    expect(posts).toBe(0);
  });

  it('edits a definition, propagating its interval to every projection', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'GET /api/v1/admin/checks/definitions': { definitions: [checkDef()] },
      'PUT /api/v1/admin/checks/definitions/c1': (req: MockRequest) => {
        put = req.body;
        return { definition: checkDef({ interval_seconds: 600 }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'disk-data' }));
    const dialog = await screen.findByRole('dialog', { name: 'disk-data' });
    const interval = within(dialog).getByLabelText('Interval (seconds)');
    await user.clear(interval);
    await user.type(interval, '600');
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(put).toEqual({ key: 'disk-data', plugin: 'disk', interval_seconds: 600, params: {} });
    expect(await screen.findByText('Check definition disk saved')).toBeInTheDocument();
  });

  // Whole-branch review finding: this dialog only ever renders a field for
  // "service"'s units; every other plugin's params() branch returned a bare
  // {}, so saving a "disk" definition's interval (the one field this dialog
  // DOES render for it) silently cleared its stored mounts allow-list, for
  // every organization's projection, in the same PUT. The fix rounds the
  // stored params back through unchanged when the plugin itself did not
  // change, exactly like the job template dialog now does for extra vars.
  it('leaves stored disk mounts intact when editing the interval, which this dialog renders instead', async () => {
    const user = userEvent.setup();
    let put: unknown;
    const withMounts = checkDef({ params: { mounts: ['/data', '/var/lib/postgres'] } });
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'GET /api/v1/admin/checks/definitions': { definitions: [withMounts] },
      'PUT /api/v1/admin/checks/definitions/c1': (req: MockRequest) => {
        put = req.body;
        return { definition: { ...withMounts, interval_seconds: 600 } };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'disk-data' }));
    const dialog = await screen.findByRole('dialog', { name: 'disk-data' });
    const interval = within(dialog).getByLabelText('Interval (seconds)');
    await user.clear(interval);
    await user.type(interval, '600');
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(put).toMatchObject({ interval_seconds: 600, params: { mounts: ['/data', '/var/lib/postgres'] } });
  });

  // Spec decision 7, Task 7's own application: a name collision names the
  // colliding organizations, to an instance admin only.
  it('shows which organizations already use a key when creating a definition collides', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'GET /api/v1/admin/checks/definitions': { definitions: [] },
      'POST /api/v1/admin/checks/definitions': () =>
        new Response(JSON.stringify({ detail: 'name "cpu" is already used by acme, widget-co' }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add check definition' }));
    await user.type(screen.getByLabelText('Key'), 'cpu');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name "cpu" is already used by acme, widget-co');
  });

  it('deletes a definition — nothing in the schema can ever block it (spec decision 5 has no dependant for this kind)', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'GET /api/v1/admin/checks/definitions': () => ({ definitions: deleted ? [] : [checkDef()] }),
      'DELETE /api/v1/admin/checks/definitions/c1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Delete disk-data' }));
    expect(await screen.findByText('No shared check definitions yet.')).toBeInTheDocument();
  });
});

/**
 * Backlog 2.101 (and 2.100, folded into it): the round-trip fix above
 * stopped an edit from CLEARING a shared template's extra vars or a
 * shared disk check's mounts, but neither was editable here — an
 * instance admin had to set them through the API and then never touch
 * that definition through the GUI again without re-sending them by
 * hand. These are the editors that close that.
 */
describe('shared definitions can be given extra vars and disk mounts', () => {
  const repoDef = () => ({
    id: 'r1', name: 'infra-base', url: 'https://git.example/provider/infra-base.git', branch: 'main', subpath: '',
  });
  const tplDef = (over: Record<string, unknown> = {}) => ({
    id: 't1', name: 'deploy-site', shared_repo_id: 'r1', playbook_path: 'site.yml', limit: '', extra_vars: {}, ...over,
  });

  it('edits a shared template definition\'s extra vars and their schema', async () => {
    const user = userEvent.setup();
    let put: unknown;
    const withSchema = tplDef({
      extra_vars: { region: 'eu-west-1' },
      extra_vars_schema: { fields: [{ name: 'region', type: 'string' }] },
    });
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [withSchema] },
      'PUT /api/v1/admin/templates/definitions/t1': (req: MockRequest) => {
        put = req.body;
        return { definition: withSchema };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });

    // The stored schema seeds the builder, and the stored value seeds
    // the generated form — so an admin sees what is there rather than
    // an empty editor over a value they cannot see.
    const value = within(dialog).getByDisplayValue('eu-west-1');
    await user.clear(value);
    await user.type(value, 'us-east-1');
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(put).toMatchObject({
      extra_vars: { region: 'us-east-1' },
      extra_vars_schema: { fields: [{ name: 'region', type: 'string' }] },
    });
  });

  it('does not offer a secret field type, which the server always refuses here', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef({ extra_vars_schema: { fields: [{ name: 'region', type: 'string' }] } })] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'deploy-site' }));
    const dialog = await screen.findByRole('dialog', { name: 'deploy-site' });
    await user.click(within(dialog).getByLabelText('Type'));
    // A shared definition's extra vars are written into every
    // organization's own column unsealed, so the server refuses a
    // secret-typed field outright. Offering it would be offering a
    // save that cannot succeed.
    expect(within(dialog).queryByRole('option', { name: 'secret' })).toBeNull();
    expect(within(dialog).getByRole('option', { name: 'string' })).toBeInTheDocument();
  });

  it('edits a shared disk check definition\'s mounts, and an empty list means every mount', async () => {
    const user = userEvent.setup();
    const puts: unknown[] = [];
    const disk = { id: 'cd1', key: 'disk', plugin: 'disk', interval_seconds: 60, params: { mounts: ['/', '/var'] } };
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'GET /api/v1/admin/checks/definitions': { definitions: [disk] },
      'PUT /api/v1/admin/checks/definitions/cd1': (req: MockRequest) => {
        puts.push(req.body);
        return { definition: disk };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'disk' }));
    let dialog = await screen.findByRole('dialog', { name: 'disk' });
    const mounts = within(dialog).getByLabelText('Mounts (one per line, empty means every mount)');
    expect(mounts).toHaveValue('/\n/var');
    await user.clear(mounts);
    await user.type(mounts, '/srv');
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(puts[0]).toMatchObject({ plugin: 'disk', params: { mounts: ['/srv'] } });

    // Emptied: the allow-list is optional, and no list means every
    // mount — the same contract an organization's own disk check keeps.
    await user.click(await screen.findByRole('button', { name: 'disk' }));
    dialog = await screen.findByRole('dialog', { name: 'disk' });
    await user.clear(within(dialog).getByLabelText('Mounts (one per line, empty means every mount)'));
    await user.click(within(dialog).getByRole('button', { name: 'Save definition' }));
    expect(puts[1]).toMatchObject({ plugin: 'disk', params: {} });
  });
});

describe('the German counts of shared definitions', () => {
  it.each([
    ['repos', 'Repository-Definition', 'Repository-Definitionen'],
    ['templates', 'Job-Vorlagen-Definition', 'Job-Vorlagen-Definitionen'],
    ['checks', 'Check-Definition', 'Check-Definitionen'],
  ] as const)('%s: one is singular, any other number plural', (kind, singular, plural) => {
    const count = de.instance.shared[kind].count;
    expect(count(1)).toMatch(new RegExp(`^1 ${singular} · `));
    expect(count(2)).toMatch(new RegExp(`^2 ${plural} · `));
    expect(count(0)).toMatch(new RegExp(`^0 ${plural} · `));
  });
});
