import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { pickOption } from '../../../test/pickOption.ts';
import { ADMIN, renderRoutes } from '../../../test/renderRoutes.tsx';
import { SharedTemplateFormPage } from './SharedTemplateFormPage.tsx';
import { settled } from '../../../test/settled.ts';

const LIST = '/o/acme/settings/shared/templates';

const repoDef = () => ({
  id: 'r1', name: 'infra-base', url: 'https://git.example/provider/infra-base.git', branch: 'main', subpath: '',
});
const tplDef = (over: Record<string, unknown> = {}) => ({
  id: 't1', name: 'deploy-site', shared_repo_id: 'r1', playbook_path: 'site.yml', limit: '', extra_vars: {}, ...over,
});

function renderPage(path: string) {
  return renderRoutes(
    [
      { path: '/settings/shared/templates', component: () => <div>the list</div> },
      { path: '/settings/shared/templates/new', component: SharedTemplateFormPage },
      { path: '/settings/shared/templates/$id', component: SharedTemplateFormPage },
    ],
    path,
  );
}

afterEach(() => vi.restoreAllMocks());

describe('SharedTemplateFormPage — new', () => {
  it('posts a new definition and goes back to the list', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
      'POST /api/v1/admin/templates/definitions': (req: MockRequest) => {
        posted = req.body;
        return { definition: tplDef({ id: 't9', ...(req.body as Record<string, unknown>) }) };
      },
    });
    const router = renderPage(`${LIST}/new`);
    await user.type(await screen.findByLabelText('Name'), 'deploy-site');
    await pickOption(screen, 'Shared repository', { value: 'r1' });
    await user.type(screen.getByLabelText('Playbook path'), 'site.yml');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));

    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
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
    await renderAndWait(`${LIST}/new`);
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findAllByText('required')).toHaveLength(3);
    expect(posts).toBe(0);
  });

  it('shows which organizations already use a name when the create collides', async () => {
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
    await renderAndWait(`${LIST}/new`);
    await user.type(screen.getByLabelText('Name'), 'deploy-site');
    await pickOption(screen, 'Shared repository', { value: 'r1' });
    await user.type(screen.getByLabelText('Playbook path'), 'site.yml');
    await user.click(screen.getByRole('button', { name: 'Create definition' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name "deploy-site" is already used by acme, widget-co');
  });

  it('asks before leaving with unsaved changes, and stays when declined', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
    });
    const router = await renderAndWait(`${LIST}/new`);
    await user.type(screen.getByLabelText('Name'), 'deploy-site');
    await user.click(screen.getByRole('link', { name: 'Cancel' }));
    const dialog = await screen.findByRole('dialog', { name: 'Are you sure?' });
    expect(dialog).toHaveTextContent('Leave this page?');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await settled();
    expect(router.state.location.pathname).toBe(`${LIST}/new`);
  });

  it('renders the extra vars editor in a section of its own', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
    });
    await renderAndWait(`${LIST}/new`);
    const section = screen.getByRole('region', { name: 'Extra vars' });
    expect(within(section).getByRole('button', { name: 'Add field' })).toBeInTheDocument();
    // The editor is the variable editor, not part of the plain fields.
    expect(within(section).queryByLabelText('Name')).not.toBeInTheDocument();
  });
});

async function renderAndWait(path: string) {
  const router = renderPage(path);
  await screen.findByLabelText('Name');
  return router;
}

describe('SharedTemplateFormPage — repository list', () => {
  it('shows the reason instead of an empty picker when the repository list cannot be loaded', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': () =>
        new Response(JSON.stringify({ detail: 'repository store unavailable' }), {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        }),
      'GET /api/v1/admin/templates/definitions': { definitions: [] },
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByText('repository store unavailable')).toBeInTheDocument();
    expect(screen.queryByLabelText('Shared repository')).not.toBeInTheDocument();
  });

  it('shows the access-denied page on the new page to someone who is not an instance administrator', async () => {
    const denied = () => new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 });
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': denied,
      'GET /api/v1/admin/templates/definitions': denied,
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});

describe('SharedTemplateFormPage — edit', () => {
  it('loads the definition, puts the change and goes back to the list', async () => {
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
    const router = renderPage(`${LIST}/t1`);
    expect(await screen.findByRole('heading', { name: 'deploy-site', level: 1 })).toBeInTheDocument();
    await user.type(screen.getByLabelText('Limit pattern'), 'web*');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));

    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
    expect(put).toEqual({
      name: 'deploy-site', shared_repo_id: 'r1', playbook_path: 'site.yml', limit: 'web*', extra_vars: {},
    });
    expect(await screen.findByText('Job template definition deploy-site saved')).toBeInTheDocument();
    expect(document.querySelector('dialog[open]')).toBeNull();
  });

  // The PUT is a full replace: an edit that does not touch the extra vars
  // must leave them exactly as they were, for every organization's
  // projection (whole-branch review finding of v0.2f).
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
    renderPage(`${LIST}/t1`);
    await user.type(await screen.findByLabelText('Limit pattern'), 'web*');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));
    await waitFor(() => expect(put).toBeDefined());
    expect(put).toMatchObject({ limit: 'web*', extra_vars: { region: 'eu-west-1', replicas: 3 } });
  });

  it("edits a shared template definition's extra vars and their schema", async () => {
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
    renderPage(`${LIST}/t1`);
    // The stored schema seeds the builder, and the stored value seeds the
    // generated form — an admin sees what is there rather than an empty
    // editor over a value they cannot see.
    const value = await screen.findByDisplayValue('eu-west-1');
    await user.clear(value);
    await user.type(value, 'us-east-1');
    await user.click(screen.getByRole('button', { name: 'Save definition' }));
    await waitFor(() => expect(put).toBeDefined());
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
      'GET /api/v1/admin/templates/definitions': {
        definitions: [tplDef({ extra_vars_schema: { fields: [{ name: 'region', type: 'string' }] } })],
      },
    });
    renderPage(`${LIST}/t1`);
    await user.click(await screen.findByRole('button', { name: 'region' }));
    await user.click(within(screen.getByRole('dialog', { name: 'Field region' })).getByLabelText('Type'));
    // A shared definition's extra vars are written into every
    // organization's own column unsealed, so offering a secret-typed field
    // would be offering a save that cannot succeed.
    expect(screen.queryByRole('option', { name: 'secret' })).toBeNull();
    expect(screen.getByRole('option', { name: 'string' })).toBeInTheDocument();
  });

  // Backlog 7.74 fix round 1: the schema editor and SchemaForm must not make an
  // untouched form look changed (by normalizing the schema or the values on
  // mount), or every visit to a template with extra vars would end in an
  // unsaved-changes question.
  it('leaves a template with an extra vars schema and values without asking when nothing was touched', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': {
        definitions: [
          tplDef({
            extra_vars: { region: 'eu-west-1', replicas: 3, debug: true },
            extra_vars_schema: {
              fields: [
                { name: 'region', type: 'string' },
                { name: 'replicas', type: 'int' },
                { name: 'debug', type: 'bool' },
              ],
            },
          }),
        ],
      },
    });
    const router = renderPage(`${LIST}/t1`);
    await screen.findByDisplayValue('eu-west-1');
    await user.click(screen.getByRole('link', { name: 'Cancel' }));
    await waitFor(() => expect(router.state.location.pathname).toBe(LIST));
    expect(document.querySelector('dialog[open]')).toBeNull();
  });

  it('shows the not-found page for an id that does not exist', async () => {
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': { definitions: [tplDef()] },
    });
    renderPage(`${LIST}/nope`);
    expect(await screen.findByText('Not found')).toBeInTheDocument();
  });

  it('shows the access-denied page to someone who is not an instance administrator', async () => {
    const denied = () => new Response(JSON.stringify({ detail: 'instance admin required' }), { status: 403 });
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': denied,
      'GET /api/v1/admin/templates/definitions': denied,
    });
    renderPage(`${LIST}/t1`);
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
  });
});

describe('SharedTemplateFormPage — create gate', () => {
  // The create page needs no definitions list: it only checks that the
  // account may create one, from /auth/me, so a failing list cannot block it.
  it('shows the empty form without requesting the definitions list', async () => {
    let listed = 0;
    mockApi({
      'GET /api/v1/auth/me': ADMIN,
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': () => {
        listed += 1;
        return new Response(JSON.stringify({ detail: 'boom' }), { status: 500 });
      },
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByRole('heading', { name: 'New job template definition', level: 1 })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create definition' })).toBeInTheDocument();
    expect(listed).toBe(0);
  });

  it('shows the access-denied page to a non-admin without requesting the definitions list', async () => {
    let listed = 0;
    mockApi({
      'GET /api/v1/auth/me': { ...ADMIN, instance_admin: false },
      'GET /api/v1/admin/repos/definitions': { definitions: [repoDef()] },
      'GET /api/v1/admin/templates/definitions': () => {
        listed += 1;
        return { definitions: [] };
      },
    });
    renderPage(`${LIST}/new`);
    expect(await screen.findByText('You do not have access')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Create definition' })).not.toBeInTheDocument();
    expect(listed).toBe(0);
  });
});
