import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { settled } from '../../test/settled.ts';
import { SecretsPage } from './SecretsPage.tsx';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });

const secret = (over: Record<string, unknown> = {}) => ({
  name: 'switch-core',
  field_names: ['auth_password', 'auth_protocol', 'username'],
  created_at: '2026-11-01T10:00:00Z',
  updated_at: '2026-11-10T10:00:00Z',
  used_by: ['snmp', 'snmp-core'],
  ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/settings/secrets', component: SecretsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/secrets'] }),
  });
  render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return qc;
}

/** A problem response as the API answers a refusal. */
function problem(status: number, detail: string, errors?: { location: string; message: string }[]) {
  return new Response(JSON.stringify({ status, detail, ...(errors ? { errors } : {}) }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });
}

async function chooseFromMenu(user: ReturnType<typeof userEvent.setup>, name: string, item: string) {
  await user.click(await screen.findByRole('button', { name: `Actions for ${name}` }));
  await user.click(await screen.findByRole('menuitem', { name: item }));
}

/** Whether anything TanStack Query still holds — a query's data or a
 * mutation's variables or result — carries the text. */
function cacheCarries(qc: QueryClient, text: string): boolean {
  const queries = qc.getQueryCache().getAll().map((q) => q.state.data);
  const mutations = qc.getMutationCache().getAll().flatMap((m) => [m.state.variables, m.state.data]);
  return [...queries, ...mutations].some((v) => JSON.stringify(v ?? null).includes(text));
}

describe('SecretsPage', () => {
  it('lists each secret with its field names, last change and the checks using it', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/secrets': {
        secrets: [secret(), secret({ name: 'ups', field_names: ['community'], used_by: [] })],
      },
    });
    renderPage();
    expect(await screen.findByText('switch-core')).toBeInTheDocument();
    expect(screen.getByText('auth_password, auth_protocol, username')).toBeInTheDocument();
    expect(screen.getByText('snmp, snmp-core')).toBeInTheDocument();
    expect(screen.getByText('ups')).toBeInTheDocument();
    expect(screen.getByText('community')).toBeInTheDocument();
    expect(screen.getByText('no check')).toBeInTheDocument();
    expect(screen.getAllByText(new Date('2026-11-10T10:00:00Z').toLocaleDateString('en'))).toHaveLength(2);
    expect(screen.getByText('Last change')).toBeInTheDocument();
  });

  it('says what a secret is for while there is none', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org('admin'), 'GET /api/v1/orgs/acme/secrets': { secrets: [] } });
    renderPage();
    expect(await screen.findByText(/No secrets yet/)).toBeInTheDocument();
  });

  it('offers an operator the list only', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org('operator'), 'GET /api/v1/orgs/acme/secrets': { secrets: [secret()] } });
    renderPage();
    expect(await screen.findByText('switch-core')).toBeInTheDocument();
    expect(await screen.findByText('Only owners and admins can create, replace or delete secrets.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New secret' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Actions for switch-core' })).not.toBeInTheDocument();
  });

  it('creates a secret from its rows, says values are never shown again and keeps no value cached', async () => {
    const user = userEvent.setup();
    const puts: MockRequest[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [] },
      'PUT /api/v1/orgs/acme/secrets/ups-1': (req: MockRequest) => {
        puts.push(req);
        return { secret: secret({ name: 'ups-1', field_names: ['auth_protocol', 'username'], used_by: [] }) };
      },
    });
    const qc = renderPage();
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    const dialog = await screen.findByRole('dialog', { name: 'New secret' });
    expect(within(dialog).getByText(/never shown again/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Name'), 'ups-1');
    await user.type(within(dialog).getByLabelText('Field 1 name'), 'username');
    expect(within(dialog).getByLabelText('Field 1 value')).toHaveAttribute('type', 'password');
    expect(within(dialog).getByLabelText('Field 1 value')).toHaveAttribute('autocomplete', 'new-password');
    await user.type(within(dialog).getByLabelText('Field 1 value'), 'pssst-monitor');
    await user.click(within(dialog).getByRole('button', { name: 'Add field' }));
    expect(within(dialog).getByLabelText('Field 2 value')).toHaveAttribute('autocomplete', 'new-password');
    await user.type(within(dialog).getByLabelText('Field 2 name'), 'auth_protocol');
    await user.type(within(dialog).getByLabelText('Field 2 value'), 'SHA');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].body).toEqual({ fields: { username: 'pssst-monitor', auth_protocol: 'SHA' } });
    // A create never replaces a secret someone else created meanwhile.
    expect(puts[0].headers.get('If-None-Match')).toBe('*');
    expect(await screen.findByText('Secret ups-1 saved')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'New secret' })).not.toBeInTheDocument());
    await settled();
    expect(cacheCarries(qc, 'pssst-monitor')).toBe(false);
  });

  it('removes a field row, never below one', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme': org('owner'), 'GET /api/v1/orgs/acme/secrets': { secrets: [] } });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    const dialog = await screen.findByRole('dialog', { name: 'New secret' });
    expect(within(dialog).getByRole('button', { name: 'Remove field 1' })).toBeDisabled();
    await user.click(within(dialog).getByRole('button', { name: 'Add field' }));
    await user.type(within(dialog).getByLabelText('Field 2 name'), 'username');
    await user.click(within(dialog).getByRole('button', { name: 'Remove field 1' }));
    expect(within(dialog).getByLabelText('Field 1 name')).toHaveValue('username');
    expect(within(dialog).queryByLabelText('Field 2 name')).not.toBeInTheDocument();
  });

  it('refuses an invalid or taken name and an empty value before the round trip', async () => {
    const user = userEvent.setup();
    const puts: MockRequest[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [secret()] },
      'PUT /api/v1/orgs/acme/secrets/switch-core': (req: MockRequest) => {
        puts.push(req);
        return { secret: secret() };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    const dialog = await screen.findByRole('dialog', { name: 'New secret' });
    await user.type(within(dialog).getByLabelText('Name'), 'bad name');
    await user.type(within(dialog).getByLabelText('Field 1 name'), 'community');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(within(dialog).getByLabelText('Name')).toHaveAttribute('aria-invalid', 'true');
    expect(within(dialog).getByText('must be 1 to 64 characters: letters, digits and . _ -')).toBeInTheDocument();
    expect(within(dialog).getByLabelText('Field 1 value')).toHaveAttribute('aria-invalid', 'true');

    await user.clear(within(dialog).getByLabelText('Name'));
    await user.type(within(dialog).getByLabelText('Name'), 'switch-core');
    await user.type(within(dialog).getByLabelText('Field 1 value'), 'x');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(within(dialog).getByText(/already exists/)).toBeInTheDocument();
    await settled();
    expect(puts).toHaveLength(0);
  });

  it('replaces a secret only with every value entered again, never prefilled', async () => {
    const user = userEvent.setup();
    const puts: MockRequest[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [secret({ field_names: ['auth_password', 'username'] })] },
      'PUT /api/v1/orgs/acme/secrets/switch-core': (req: MockRequest) => {
        puts.push(req);
        return { secret: secret() };
      },
    });
    const qc = renderPage();
    await chooseFromMenu(user, 'switch-core', 'Replace');
    const dialog = await screen.findByRole('dialog', { name: 'Replace switch-core' });
    expect(within(dialog).getByText(/enter every value again/)).toBeInTheDocument();
    expect(within(dialog).getByText(/never shown again/)).toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Name')).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText('Field 1 name')).toHaveValue('auth_password');
    expect(within(dialog).getByLabelText('Field 2 name')).toHaveValue('username');
    expect(within(dialog).getByLabelText('Field 1 value')).toHaveValue('');
    expect(within(dialog).getByLabelText('Field 2 value')).toHaveValue('');

    await user.type(within(dialog).getByLabelText('Field 1 value'), 'new-auth-password');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(within(dialog).getByLabelText('Field 2 value')).toHaveAttribute('aria-invalid', 'true');
    await settled();
    expect(puts).toHaveLength(0);

    await user.type(within(dialog).getByLabelText('Field 2 value'), 'ops');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].body).toEqual({ fields: { auth_password: 'new-auth-password', username: 'ops' } });
    expect(puts[0].headers.get('If-None-Match')).toBeNull();
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Replace switch-core' })).not.toBeInTheDocument());
    await settled();
    expect(cacheCarries(qc, 'new-auth-password')).toBe(false);
  });

  it("shows the server's refusal in the dialog and still keeps the values out of the cache", async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [] },
      'PUT /api/v1/orgs/acme/secrets/sw': () =>
        new Response(JSON.stringify({ detail: 'invalid secret: field community: the value is not valid UTF-8' }), {
          status: 422,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    const qc = renderPage();
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    const dialog = await screen.findByRole('dialog', { name: 'New secret' });
    await user.type(within(dialog).getByLabelText('Name'), 'sw');
    await user.type(within(dialog).getByLabelText('Field 1 name'), 'community');
    await user.type(within(dialog).getByLabelText('Field 1 value'), 'refused-value');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('invalid secret: field community: the value is not valid UTF-8');
    expect(cacheCarries(qc, 'refused-value')).toBe(false);
  });

  it('deletes an unused secret after asking', async () => {
    const user = userEvent.setup();
    let deleted = false;
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [secret({ name: 'ups', used_by: [] })] },
      'DELETE /api/v1/orgs/acme/secrets/ups': () => {
        deleted = true;
        return {};
      },
    });
    renderPage();
    await chooseFromMenu(user, 'ups', 'Delete');
    expect(await acceptConfirm(user)).toBe('Delete secret ups? This cannot be undone.');
    await waitFor(() => expect(deleted).toBe(true));
    expect(await screen.findByText('Secret ups deleted')).toBeInTheDocument();
  });

  it('names the checks of a 409 that the stale list did not know about', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [secret({ used_by: [] }), secret({ name: 'ups', used_by: [] })] },
      'DELETE /api/v1/orgs/acme/secrets/switch-core': () =>
        problem(409, 'secret in use: the checks snmp, snmp-core use it', [
          { location: 'used_by', message: 'snmp' },
          { location: 'used_by', message: 'snmp-core' },
        ]),
      'DELETE /api/v1/orgs/acme/secrets/ups': () =>
        problem(409, 'secret in use: the checks snmp-ups use it', [{ location: 'used_by', message: 'snmp-ups' }]),
    });
    renderPage();
    await chooseFromMenu(user, 'switch-core', 'Delete');
    await acceptConfirm(user);
    expect(
      await screen.findByText('Secret switch-core is still used by the checks snmp, snmp-core and was not deleted.'),
    ).toBeInTheDocument();
    await chooseFromMenu(user, 'ups', 'Delete');
    await acceptConfirm(user);
    expect(await screen.findByText('Secret ups is still used by the check snmp-ups and was not deleted.')).toBeInTheDocument();
  });

  it("shows the server's own text for any other refused delete", async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [secret({ used_by: [] })] },
      'DELETE /api/v1/orgs/acme/secrets/switch-core': () => problem(500, 'internal error'),
    });
    renderPage();
    await chooseFromMenu(user, 'switch-core', 'Delete');
    await acceptConfirm(user);
    expect(await screen.findByText('internal error')).toBeInTheDocument();
  });

  it('explains up front that checks use a secret instead of offering to delete it', async () => {
    const user = userEvent.setup();
    let deletes = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [secret(), secret({ name: 'ups', used_by: ['snmp-ups'] })] },
      'DELETE /api/v1/orgs/acme/secrets/switch-core': () => {
        deletes += 1;
        return {};
      },
    });
    renderPage();
    await chooseFromMenu(user, 'switch-core', 'Delete');
    const dialog = await screen.findByRole('dialog', { name: 'Secret in use' });
    expect(
      within(dialog).getByText('Secret switch-core is used by the checks snmp, snmp-core. Change those checks to name another secret, or delete them, before deleting it.'),
    ).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Secret in use' })).not.toBeInTheDocument());

    await chooseFromMenu(user, 'ups', 'Delete');
    expect(
      await screen.findByText('Secret ups is used by the check snmp-ups. Change that check to name another secret, or delete it, before deleting the secret.'),
    ).toBeInTheDocument();
    await settled();
    expect(deletes).toBe(0);
  });

  it('says so when a create finds the name taken meanwhile, without replacing it', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/secrets': { secrets: [] },
      'PUT /api/v1/orgs/acme/secrets/sw': () => problem(412, 'a secret of this name already exists'),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    const dialog = await screen.findByRole('dialog', { name: 'New secret' });
    await user.type(within(dialog).getByLabelText('Name'), 'sw');
    await user.type(within(dialog).getByLabelText('Field 1 name'), 'community');
    await user.type(within(dialog).getByLabelText('Field 1 value'), 'x');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'A secret named sw was created in the meantime. Choose another name, or close this dialog and replace sw from its row.',
    );
  });

  it('shows empty values after cancelling and opening the dialog again', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme': org('admin'), 'GET /api/v1/orgs/acme/secrets': { secrets: [secret()] } });
    renderPage();
    await chooseFromMenu(user, 'switch-core', 'Replace');
    let dialog = await screen.findByRole('dialog', { name: 'Replace switch-core' });
    await user.type(within(dialog).getByLabelText('Field 1 value'), 'typed-then-cancelled');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Replace switch-core' })).not.toBeInTheDocument());
    await chooseFromMenu(user, 'switch-core', 'Replace');
    dialog = await screen.findByRole('dialog', { name: 'Replace switch-core' });
    expect(within(dialog).getByLabelText('Field 1 value')).toHaveValue('');

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    dialog = await screen.findByRole('dialog', { name: 'New secret' });
    await user.type(within(dialog).getByLabelText('Field 1 value'), 'typed-then-cancelled');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    dialog = await screen.findByRole('dialog', { name: 'New secret' });
    expect(within(dialog).getByLabelText('Field 1 value')).toHaveValue('');
  });

  it('groups the field rows under their caption and hint', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme': org('admin'), 'GET /api/v1/orgs/acme/secrets': { secrets: [] } });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    const group = await screen.findByRole('group', { name: 'Fields' });
    expect(group).toHaveAccessibleDescription(/For SNMP v2c: community/);
    expect(within(group).getByLabelText('Field 1 value')).toBeInTheDocument();
  });

  it('shows a value while typing on request, hidden by default', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme': org('admin'), 'GET /api/v1/orgs/acme/secrets': { secrets: [] } });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New secret' }));
    const dialog = await screen.findByRole('dialog', { name: 'New secret' });
    await user.click(within(dialog).getByRole('button', { name: 'Add field' }));
    const show = within(dialog).getByRole('button', { name: 'Show value of field 1' });
    expect(show).toHaveAttribute('aria-pressed', 'false');
    await user.click(show);
    expect(within(dialog).getByLabelText('Field 1 value')).toHaveAttribute('type', 'text');
    expect(within(dialog).getByLabelText('Field 2 value')).toHaveAttribute('type', 'password');
    expect(within(dialog).getByRole('button', { name: 'Show value of field 1' })).toHaveAttribute('aria-pressed', 'true');
    await user.click(within(dialog).getByRole('button', { name: 'Show value of field 1' }));
    expect(within(dialog).getByLabelText('Field 1 value')).toHaveAttribute('type', 'password');
  });
});
