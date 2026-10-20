import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { ReposPage } from './ReposPage.tsx';

const ORG = { org: { id: 'org-1', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
// credentials.write is admin-only (lib/roles.ts), stricter than the
// repos.write an operator already has — the credential create rows'
// own permission gate (v0.4g Task 7 fix round) needs this role, not ORG's.
const ADMIN_ORG = { org: { ...ORG.org, role: 'admin' } };
const gitKey = { id: 'c1', name: 'deploy-key', kind: 'git_key', username: 'git', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };
const sshKey = { id: 'c2', name: 'host-login', kind: 'ssh_key', username: 'ansible', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 3 }, created_at: '', updated_at: '' };
const vaultProd = { id: 'c3', name: 'vault-prod', kind: 'vault_password', username: '', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };
const vaultStaging = { id: 'c4', name: 'vault-staging', kind: 'vault_password', username: '', expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '' };

const repo = (over: Record<string, unknown> = {}) => ({
  id: 'r1', name: 'infra', url: 'https://git.example/acme/infra.git', branch: 'main', subpath: '',
  credential_id: 'c1', created_at: '', updated_at: '', ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const reposRoute = createRoute({ getParentRoute: () => orgRoute, path: '/repos', component: ReposPage });
  const jobRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([reposRoute, jobRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/repos'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('ReposPage', () => {
  it('lists repositories with url, branch, credential and sync state', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo({ last_sync: '2026-09-15T10:00:00Z' })] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, sshKey] },
    });
    renderPage();
    expect(await screen.findByText('infra')).toBeInTheDocument();
    expect(screen.getByText('https://git.example/acme/infra.git')).toBeInTheDocument();
    expect(screen.getByText('main')).toBeInTheDocument();
    expect(await screen.findByText(/deploy-key/)).toBeInTheDocument();
    expect(screen.getByText(/^synced 2026-09-15/)).toBeInTheDocument();
  });

  it('marks a failed sync in crit with its error as the title', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo({ last_sync: '2026-09-15T10:00:00Z', last_sync_error: 'authentication failed' })] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    const pill = await screen.findByText('failed');
    expect(pill).toHaveStyle({ color: 'var(--px-crit)' });
    expect(screen.getByTitle('authentication failed')).toBeInTheDocument();
  });

  it('warns for a repository that has never been synced', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo()] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    expect(await screen.findByText('never synced')).toBeInTheDocument();
  });

  it('creates a repository and offers git credentials only', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const repos: Record<string, unknown>[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': () => ({ repos }),
      'POST /api/v1/orgs/acme/repos': (req: MockRequest) => {
        posted = req.body;
        const created = repo({ id: 'r2', name: 'new-repo' });
        repos.push(created);
        return { repo: created };
      },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, sshKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.type(screen.getByLabelText('Name'), 'new-repo');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/new.git');

    // The credential picker offered the git key, not the SSH key — opened
    // by hand (rather than through pickOption) so the full row list can be
    // read before committing to one.
    await user.click(screen.getByLabelText('Credential'));
    const rows = screen
      .getAllByRole('option')
      .filter((o) => o.getAttribute('data-value') !== '' && o.getAttribute('data-label') !== 'New credential…');
    expect(rows.map((o) => o.getAttribute('data-value'))).toEqual(['c1']);
    await user.click(rows[0]);

    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(await screen.findByText('new-repo')).toBeInTheDocument();
    expect(posted).toEqual({ name: 'new-repo', url: 'https://git.example/acme/new.git', branch: 'main', subpath: '', credential_id: 'c1' });
  });

  // v0.4g Task 7: the Credential picker's create row opens AddCredentialDialog,
  // seeded with whatever was typed before the row was taken, and reports the
  // new id back to the picker as soon as the credential exists — not only
  // once the one-time public-key panel (shown here, since a generated key
  // credential always returns one) is dismissed. That timing choice is the
  // point of this test: the panel is still open when the assertion against
  // the picker's own value runs.
  it('creates a credential from the picker and selects it, before the one-time key panel is dismissed', async () => {
    const user = userEvent.setup();
    let repoPosted: unknown;
    let credPosted: unknown;
    const zetaDeploy = {
      id: 'c9', name: 'zeta-deploy', kind: 'git_key', username: '', expired: false, expires_soon: false,
      used_by: { bastions: 0, hosts: 0 }, created_at: '', updated_at: '', public_key: 'ssh-ed25519 AAAA pulsix:acme:zeta-deploy',
    };
    let created = false;
    mockApi({
      // Admin: credentials.write is required for the create row to appear
      // at all (v0.4g Task 7 fix round) — an operator would not see it.
      'GET /api/v1/orgs/acme': ADMIN_ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      // A function, not a fixed value: the Credential picker's options
      // come from this query's own refetch after the credential's create
      // invalidates it, not from onCreated's id alone (v0.4g Task 5 hit the
      // same thing with a fixed bastions list — "b9 (unavailable)").
      'GET /api/v1/orgs/acme/credentials': () => ({ credentials: created ? [gitKey, zetaDeploy] : [gitKey] }),
      'POST /api/v1/orgs/acme/credentials': (req: MockRequest) => {
        credPosted = req.body;
        created = true;
        return {
          credential: zetaDeploy,
          snippets: { authorized_keys_command: 'echo', cloud_init: '#cloud-config', terraform: 'locals {}' },
        };
      },
      'POST /api/v1/orgs/acme/repos': (req: MockRequest) => {
        repoPosted = req.body;
        return { repo: repo({ id: 'r9', name: 'new-repo', credential_id: 'c9' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.type(screen.getByLabelText('Name'), 'new-repo');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/new.git');

    await user.click(screen.getByLabelText('Credential'));
    await user.keyboard('zeta-deploy');
    await user.click(screen.getByRole('option', { name: /New credential/ }));

    const addDialog = await screen.findByRole('dialog', { name: 'Add credential' });
    expect(within(addDialog).getByLabelText('Name')).toHaveValue('zeta-deploy');
    // RepoDialog's own Credential field only offers git_key/git_token —
    // AddCredentialDialog defaults to ssh_key, which would not match.
    await pickOption(within(addDialog), 'Kind', { value: 'git_key' });
    await user.click(within(addDialog).getByRole('button', { name: 'Create credential' }));

    expect(credPosted).toMatchObject({ name: 'zeta-deploy', kind: 'git_key', generate: true });
    expect(await screen.findByRole('dialog', { name: 'Install the public key' })).toBeInTheDocument();
    expect(screen.getByLabelText('Credential')).toHaveValue('zeta-deploy');

    await user.click(screen.getByRole('button', { name: 'Done' }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(repoPosted).toMatchObject({ credential_id: 'c9' });
  });

  // v0.4g Task 7 fix round, Important 1: credentials.write is admin
  // (lib/roles.ts), stricter than the repos.write that already lets an
  // operator open this whole dialog. Offering a create row an operator
  // cannot actually use (the POST 403s) violates the spec's own condition
  // 1 for a create row existing at all — so the row itself must not
  // render, not merely appear disabled.
  it('offers the Credential create row to an admin but not an operator', async () => {
    const user = userEvent.setup();

    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    const { unmount } = renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.click(screen.getByLabelText('Credential'));
    expect(screen.queryByRole('option', { name: /New credential/ })).not.toBeInTheDocument();
    unmount();

    mockApi({
      'GET /api/v1/orgs/acme': ADMIN_ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.click(screen.getByLabelText('Credential'));
    expect(screen.getByRole('option', { name: /New credential/ })).toBeInTheDocument();
  });

  // v0.4g Task 7 fix round, Important 2: AddCredentialDialog defaults Kind
  // to ssh_key, which Vault password's own filter (vault_password only)
  // excludes — without initialKind, a credential created from this row
  // would show as "(unavailable)" in the very field that just made it.
  it('opens the credential dialog with Vault password already chosen from that create row', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ADMIN_ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));

    await user.click(screen.getByLabelText('Vault password'));
    await user.keyboard('prod-vault');
    await user.click(screen.getByRole('option', { name: /New credential/ }));

    const addDialog = await screen.findByRole('dialog', { name: 'Add credential' });
    expect(within(addDialog).getByLabelText('Name')).toHaveValue('prod-vault');
    expect(within(addDialog).getByLabelText('Kind')).toHaveValue('Ansible Vault password');
  });

  it('shows the server-mirror validation message at the field without a round trip', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'POST /api/v1/orgs/acme/repos': (req: MockRequest) => {
        posted.push(req.body);
        return { repo: repo() };
      },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.type(screen.getByLabelText('Name'), 'Bad Name');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/new.git');
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid name: must be lowercase letters');
    expect(posted).toEqual([]);
  });

  it('shows a server 409 (name taken) at the name field', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [] },
      'POST /api/v1/orgs/acme/repos': () =>
        new Response(JSON.stringify({ status: 409, detail: 'name already in use' }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add repository' }));
    await user.type(screen.getByLabelText('Name'), 'infra');
    await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/infra.git');
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('name already in use');
  });

  it('syncs a repository and links to the run view', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo()] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      'POST /api/v1/orgs/acme/repos/r1/sync': { job: { id: 'job-1', zone_id: 'z1', kind: 'sync_repo', status: 'queued', run_group_id: 'g1', trigger: 'manual', created_at: '' } },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Sync infra' }));
    expect(await screen.findByText('RUN VIEW')).toBeInTheDocument();
  });

  it('edits a repository through the dialog', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo()] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      'PUT /api/v1/orgs/acme/repos/r1': (req: MockRequest) => {
        put = req.body;
        return { repo: repo({ branch: 'release' }) };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'infra' }));
    const dialog = await screen.findByRole('dialog', { name: 'infra' });
    const branch = within(dialog).getByLabelText('Branch');
    await user.clear(branch);
    await user.type(branch, 'release');
    await user.click(within(dialog).getByRole('button', { name: 'Save repository' }));
    expect(put).toEqual({ name: 'infra', url: 'https://git.example/acme/infra.git', branch: 'release', subpath: '', credential_id: 'c1' });
  });

  it('deletes a repository after confirmation', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = false;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': () => ({ repos: deleted ? [] : [repo()] }),
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      'DELETE /api/v1/orgs/acme/repos/r1': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'infra' }));
    const dialog = await screen.findByRole('dialog', { name: 'infra' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    expect(await screen.findByText('No repositories yet.')).toBeInTheDocument();
  });

  it('shows the git server key pin in the dialog and unpins it', async () => {
    const user = userEvent.setup();
    let cleared = false;
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': () => ({
        repos: cleared ? [repo()] : [repo({ ssh_host_key: 'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', pin_state: 'full' })],
      }),
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      'DELETE /api/v1/orgs/acme/repos/r1/host-key': () => {
        cleared = true;
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'infra' }));
    const dialog = await screen.findByRole('dialog', { name: 'infra' });
    expect(within(dialog).getByText('SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Unpin key' }));
    // The unpin reloads the registry, and the refreshed dialog shows the
    // repository went back to trust on first use.
    expect(await within(dialog).findByText(/not pinned yet/)).toBeInTheDocument();
  });

  it('shows an unpinned repository without pin controls', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': ORG,
      'GET /api/v1/orgs/acme/repos': { repos: [repo()] },
      'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'infra' }));
    const dialog = await screen.findByRole('dialog', { name: 'infra' });
    expect(within(dialog).getByText(/not pinned yet/)).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Unpin key' })).not.toBeInTheDocument();
  });

  // v0.2f Task 5: a repository that is a projection of an installation-owned
  // definition (spec decisions 1, 2, 6). Its url/branch/subpath — and name,
  // which the spec's own table gives to the definition for every resource
  // type (decisions 2) just as much as the other three — stay visible but
  // read-only; its credential and the delete action are not: a credential
  // never belongs to the definition (decision 3), and decision 6 refuses a
  // delete from inside an organization outright, so offering the button
  // here would only ever produce a refusal (the same reasoning the brief
  // gives for leaving it off).
  describe('installation-managed projection (v0.2f)', () => {
    it('marks a projected repository in the list and the dialog, disables its shared fields, and offers no delete action', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/repos': { repos: [repo({ shared_repo_id: 'def-1' })] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      });
      renderPage();

      expect(await screen.findByText('Installation-managed')).toBeInTheDocument();

      await user.click(screen.getByRole('button', { name: 'infra' }));
      const dialog = await screen.findByRole('dialog', { name: 'infra' });

      // The marker repeats inside the dialog — it is its own element, not
      // only the list row's.
      expect(within(dialog).getByText('Installation-managed')).toBeInTheDocument();

      // Disabled, not hidden: the value stays on screen, describing the
      // resource, even though this organization cannot change it.
      expect(within(dialog).getByLabelText('Name')).toBeDisabled();
      expect(within(dialog).getByLabelText('Name')).toHaveValue('infra');
      expect(within(dialog).getByLabelText('URL')).toBeDisabled();
      expect(within(dialog).getByLabelText('URL')).toHaveValue('https://git.example/acme/infra.git');
      expect(within(dialog).getByLabelText('Branch')).toBeDisabled();
      expect(within(dialog).getByLabelText('Subdirectory')).toBeDisabled();

      // The organization's own bindings stay editable.
      expect(within(dialog).getByLabelText('Credential')).not.toBeDisabled();

      expect(within(dialog).queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    });

    it('keeps every field editable and offers delete for an ordinary, non-projected repository', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/repos': { repos: [repo()] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey] },
      });
      renderPage();

      expect(screen.queryByText('Installation-managed')).not.toBeInTheDocument();

      await user.click(await screen.findByRole('button', { name: 'infra' }));
      const dialog = await screen.findByRole('dialog', { name: 'infra' });
      expect(within(dialog).getByLabelText('Name')).not.toBeDisabled();
      expect(within(dialog).getByLabelText('URL')).not.toBeDisabled();
      expect(within(dialog).getByLabelText('Branch')).not.toBeDisabled();
      expect(within(dialog).getByLabelText('Subdirectory')).not.toBeDisabled();
      expect(within(dialog).getByRole('button', { name: 'Delete' })).toBeInTheDocument();
    });
  });

  // v0.2e Task 5: a repository's own labelled vault password set, beside
  // the legacy single Vault password field.
  describe('vault identities', () => {
    it('adds vault identity rows, each with a label and a credential picker, and posts them', async () => {
      const user = userEvent.setup();
      let posted: unknown;
      const repos: Record<string, unknown>[] = [];
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/repos': () => ({ repos }),
        'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, vaultProd, vaultStaging] },
        'POST /api/v1/orgs/acme/repos': (req: MockRequest) => {
          posted = req.body;
          const created = repo({ id: 'r9', name: 'new-repo' });
          repos.push(created);
          return { repo: created };
        },
      });
      renderPage();
      await user.click(await screen.findByRole('button', { name: 'Add repository' }));
      await user.type(screen.getByLabelText('Name'), 'new-repo');
      await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/new.git');

      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 1 label'), 'production');
      await pickOption(screen, 'Vault identity 1 credential', { value: 'c3' });

      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 2 label'), 'staging');
      await pickOption(screen, 'Vault identity 2 credential', { value: 'c4' });

      await user.click(screen.getByRole('button', { name: 'Create repository' }));
      expect(await screen.findByText('new-repo')).toBeInTheDocument();
      expect(posted).toMatchObject({
        vault_identities: [
          { label: 'production', credential_id: 'c3' },
          { label: 'staging', credential_id: 'c4' },
        ],
      });
    });

    it('removes a vault identity row, keeping the rest', async () => {
      const user = userEvent.setup();
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/repos': { repos: [] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, vaultProd, vaultStaging] },
      });
      renderPage();
      await user.click(await screen.findByRole('button', { name: 'Add repository' }));
      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 1 label'), 'production');
      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 2 label'), 'staging');

      await user.click(screen.getByRole('button', { name: 'Remove vault identity 1' }));
      expect(screen.queryByLabelText('Vault identity 2 label')).not.toBeInTheDocument();
      expect(screen.getByLabelText('Vault identity 1 label')).toHaveValue('staging');
    });

    it('refuses a duplicate vault identity label before submitting', async () => {
      const user = userEvent.setup();
      let posted: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/repos': { repos: [] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, vaultProd, vaultStaging] },
        'POST /api/v1/orgs/acme/repos': (req: MockRequest) => {
          posted = req.body;
          return { repo: repo() };
        },
      });
      renderPage();
      await user.click(await screen.findByRole('button', { name: 'Add repository' }));
      await user.type(screen.getByLabelText('Name'), 'new-repo');
      await user.type(screen.getByLabelText('URL'), 'https://git.example/acme/new.git');

      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 1 label'), 'production');
      await pickOption(screen, 'Vault identity 1 credential', { value: 'c3' });
      await user.click(screen.getByRole('button', { name: 'Add vault password' }));
      await user.type(screen.getByLabelText('Vault identity 2 label'), 'production');
      await pickOption(screen, 'Vault identity 2 credential', { value: 'c4' });

      await user.click(screen.getByRole('button', { name: 'Create repository' }));
      expect(await screen.findAllByText('duplicate vault identity label "production"')).toHaveLength(2);
      expect(posted).toBeUndefined();
    });

    // The bug class the brief calls out by name: a hand-built save body
    // that forgets a field silently wipes it on an unrelated edit. Saving
    // a repository that already has identities, without touching them,
    // must keep sending the same set.
    it('keeps an existing repository\'s vault identities when an unrelated field is edited', async () => {
      const user = userEvent.setup();
      let put: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/repos': {
          repos: [repo({ vault_identities: [{ credential_id: 'c3', label: 'production' }] })],
        },
        'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, vaultProd] },
        'PUT /api/v1/orgs/acme/repos/r1': (req: MockRequest) => {
          put = req.body;
          return { repo: repo({ branch: 'release' }) };
        },
      });
      renderPage();
      await user.click(await screen.findByRole('button', { name: 'infra' }));
      const dialog = await screen.findByRole('dialog', { name: 'infra' });
      const branch = within(dialog).getByLabelText('Branch');
      await user.clear(branch);
      await user.type(branch, 'release');
      await user.click(within(dialog).getByRole('button', { name: 'Save repository' }));
      expect(put).toMatchObject({ vault_identities: [{ label: 'production', credential_id: 'c3' }] });
    });

    // Review item 1: legacyVaultIdentities (internal/repos/service.go)
    // ignores vault_credential_id the moment any identity row exists — a
    // repository configured only through the legacy Vault password field,
    // then given one identity row, loses that password from every run even
    // though the field still shows it as set. The dialog must stop
    // presenting that field as live the moment a row is added.
    it('disables the legacy Vault password field once a vault identity row is added, and still saves the identity', async () => {
      const user = userEvent.setup();
      let put: unknown;
      mockApi({
        'GET /api/v1/orgs/acme': ORG,
        'GET /api/v1/orgs/acme/repos': { repos: [repo({ vault_credential_id: 'c3' })] },
        'GET /api/v1/orgs/acme/credentials': { credentials: [gitKey, vaultProd, vaultStaging] },
        'PUT /api/v1/orgs/acme/repos/r1': (req: MockRequest) => {
          put = req.body;
          return { repo: repo({ vault_credential_id: 'c3' }) };
        },
      });
      renderPage();
      await user.click(await screen.findByRole('button', { name: 'infra' }));
      const dialog = await screen.findByRole('dialog', { name: 'infra' });
      const vaultSelect = within(dialog).getByLabelText('Vault password');
      expect(vaultSelect).toHaveValue('vault-prod');
      expect(vaultSelect).not.toBeDisabled();

      await user.click(within(dialog).getByRole('button', { name: 'Add vault password' }));
      await user.type(within(dialog).getByLabelText('Vault identity 1 label'), 'staging');
      await pickOption(screen, 'Vault identity 1 credential', { value: 'c4' });

      expect(vaultSelect).toBeDisabled();
      expect(within(dialog).getByText(/Ignored while any Vault identity below is set/)).toBeInTheDocument();

      await user.click(within(dialog).getByRole('button', { name: 'Save repository' }));
      expect(put).toMatchObject({ vault_identities: [{ label: 'staging', credential_id: 'c4' }] });
    });
  });
});
