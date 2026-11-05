import { describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { check, chooseFromMenu, GROUPS, org, renderPage } from './checksPageTestKit.tsx';

describe('ChecksPage', () => {
  it('lists definitions with their binding, interval, state and origin', async () => {
    const fetch = mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': {
        checks: [
          check(),
          check({ id: 'c2', key: 'disk-data', plugin: 'disk', interval_seconds: 300, origin: 'custom', binding_kind: 'group', group_id: 'g1' }),
          check({
            id: 'c3', key: 'nginx', plugin: 'service', interval_seconds: 30, enabled: false,
            origin: 'custom', binding_kind: 'host', host_id: 'h1', host_name: 'web-1',
          }),
        ],
      },
    });
    renderPage();

    // The seeded cpu row shows its key and its plugin, both "cpu".
    expect(await screen.findAllByText('cpu')).toHaveLength(2);
    // Every binding kind reads as what it binds to, never as a raw id.
    expect(screen.getByText('Organization')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Web tier' })).toBeInTheDocument();
    // The bound host's name rides on the definition (host_name), so the
    // page issues no per-row host request at all.
    expect(screen.getByRole('link', { name: 'web-1' })).toBeInTheDocument();
    const urls = fetch.mock.calls.map(([input]) => (input instanceof Request ? input.url : String(input)));
    expect(urls.filter((u) => /\/hosts\//.test(u))).toEqual([]);
    // Origin is on the row, not hidden inside a dialog.
    expect(screen.getByText('seed')).toBeInTheDocument();
    expect(screen.getAllByText('custom')).toHaveLength(2);
    expect(screen.getByText('1 min')).toBeInTheDocument();
    expect(screen.getByText('5 min')).toBeInTheDocument();
    expect(screen.getByText('30 s')).toBeInTheDocument();
    expect(screen.getByText('disabled')).toBeInTheDocument();
  });

  it('shows a viewer the definitions but no write affordances', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('viewer'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check(), check({ id: 'c2', key: 'disk-data', plugin: 'disk', origin: 'custom' })] },
    });
    renderPage();

    expect(await screen.findAllByText('cpu')).toHaveLength(2);
    expect(screen.queryByRole('button', { name: 'New check' })).not.toBeInTheDocument();
  });

  it('creates a definition with the body the API documents', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted = req.body;
        return { check: check({ id: 'c9', key: 'nginx', plugin: 'service', origin: 'custom' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Plugin', { value: 'service' });
    await user.type(screen.getByLabelText('Key'), 'nginx');
    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.type(screen.getByLabelText('Interval (seconds)'), '30');
    await user.type(screen.getByLabelText('Units (one per line)'), 'nginx\nphp-fpm');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toEqual({
        plugin: 'service',
        key: 'nginx',
        params: { units: ['nginx', 'php-fpm'] },
        interval_seconds: 30,
        enabled: true,
        binding_kind: 'org',
      }),
    );
  });

  it('keeps a seeded key and plugin read-only while still retuning its interval', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check()] },
      'PUT /api/v1/orgs/acme/checks/c1': (req: MockRequest) => {
        put = req.body;
        return { check: check({ interval_seconds: 120 }) };
      },
    });
    renderPage();

    await chooseFromMenu(user, 'cpu (Organization)', 'Edit');
    expect(screen.getByLabelText('Key')).toBeDisabled();
    expect(screen.getByLabelText('Plugin')).toBeDisabled();
    expect(screen.getByLabelText('Interval (seconds)')).not.toBeDisabled();

    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.type(screen.getByLabelText('Interval (seconds)'), '120');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(put).toEqual({ plugin: 'cpu', key: 'cpu', params: {}, interval_seconds: 120, enabled: true, binding_kind: 'org' }),
    );
  });

  it('binds a definition to a single monitored host', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [] },
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'web-1' }], next_cursor: '' },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { check: check({ id: 'c9', origin: 'custom' }) };
      },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'New check' }));
    await pickOption(screen, 'Binds to', { value: 'host' });
    await pickOption(screen, 'Host', { value: 'h1' });
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(posted).toMatchObject({ binding_kind: 'host', host_id: 'h1', plugin: 'cpu' }));
    expect(posted).not.toHaveProperty('group_id');
  });

  it('deletes a definition once the operator has confirmed it', async () => {
    const user = userEvent.setup();
    const confirmed = vi.spyOn(window, 'confirm').mockReturnValue(true);
    let deleted = 0;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c2', key: 'disk-data', plugin: 'disk', origin: 'custom' })] },
      'DELETE /api/v1/orgs/acme/checks/c2': () => {
        deleted += 1;
        return {};
      },
    });
    renderPage();

    await chooseFromMenu(user, 'disk-data (Organization)', 'Delete…');
    await waitFor(() => expect(deleted).toBe(1));
    expect(confirmed.mock.calls[0][0]).toContain('disk-data');
    confirmed.mockRestore();
  });

  // Review finding 1: a value whose option is not in the loaded list must
  // still render as itself rather than fall back silently — a Select
  // finding no matching option shows "<id> (unavailable)" instead
  // (Select.tsx's own selectedLabel comment), which here would misstate a
  // real binding as something broken. Both the fallback option and the
  // rendered value are pinned here.
  it('edits a host-bound definition whose host is past the first page of the picker', async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      // The bound host, db-7, is not on the page the picker loads.
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'web-1' }], next_cursor: 'more' },
      'GET /api/v1/orgs/acme/checks': {
        checks: [check({ id: 'c4', key: 'disk-data', plugin: 'disk', origin: 'custom', binding_kind: 'host', host_id: 'h9', host_name: 'db-7' })],
      },
      'PUT /api/v1/orgs/acme/checks/c4': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c4' }) };
      },
    });
    renderPage();

    await chooseFromMenu(user, 'disk-data (db-7)', 'Edit');
    const select = (await screen.findByLabelText('Host')) as HTMLInputElement;
    // The binding is stated as the binding, by name: the closed field shows
    // the fallback option's own label, not "h9 (unavailable)".
    expect(select.value).toBe('db-7 (current binding)');
    await user.click(select);
    expect(screen.getByRole('option', { name: 'db-7 (current binding)' })).toBeInTheDocument();
    await user.keyboard('{Escape}');

    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toMatchObject({ binding_kind: 'host', host_id: 'h9' }));
  });

  it('edits a group-bound definition even when the monitoring groups list fails', async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': () => new Response('{"detail":"boom"}', { status: 500, headers: { 'Content-Type': 'application/json' } }),
      'GET /api/v1/orgs/acme/checks': {
        checks: [check({ id: 'c5', key: 'disk-data', plugin: 'disk', origin: 'custom', binding_kind: 'group', group_id: 'g1' })],
      },
      'PUT /api/v1/orgs/acme/checks/c5': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c5' }) };
      },
    });
    renderPage();

    await chooseFromMenu(user, 'disk-data (group g1)', 'Edit');
    const select = (await screen.findByLabelText('Monitoring group')) as HTMLInputElement;
    expect(select.value).toBe('group g1 (current binding)');

    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put).toMatchObject({ binding_kind: 'group', group_id: 'g1' }));
  });

  // Review finding 3: an update with a blank key used to be answered 200
  // with the definition renamed to its plugin name. The server refuses it
  // now (internal/checks UpdateDefinition); the field's required+pattern
  // only saves the round trip — and pattern is what covers whitespace,
  // which satisfies required on its own.
  it('refuses to save an existing definition whose key was cleared or blanked, rather than renaming it', async () => {
    const user = userEvent.setup();
    let puts = 0;
    let sent: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c6', key: 'disk-data', plugin: 'disk', origin: 'custom' })] },
      'PUT /api/v1/orgs/acme/checks/c6': (req: MockRequest) => {
        puts += 1;
        sent = req.body as Record<string, unknown>;
        return { check: check({ id: 'c6' }) };
      },
    });
    renderPage();

    await chooseFromMenu(user, 'disk-data (Organization)', 'Edit');
    await user.clear(screen.getByLabelText('Key'));
    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect((screen.getByLabelText('Key') as HTMLInputElement).validity.valueMissing).toBe(true);
    expect(puts).toBe(0);

    // `required` alone is satisfied by a single space, which is why the
    // browser was never the guard. The field trims as it is typed, so
    // whitespace cannot even be entered — and what `pattern` judges is
    // therefore exactly the string the body carries.
    await user.type(screen.getByLabelText('Key'), '   ');
    expect(screen.getByLabelText('Key')).toHaveValue('');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(puts).toBe(0);

    // keyPattern, internal/checks/definitions.go — asserted as an
    // attribute, not as behaviour, because jsdom does not evaluate
    // `pattern` at all (probed: patternMismatch is false for " " and the
    // form submits).
    expect(screen.getByLabelText('Key')).toHaveAttribute('pattern', '[a-z0-9][a-z0-9-]{0,62}');

    // Surrounding whitespace is trimmed rather than refused, so the value
    // the pattern accepts and the value sent are the same string.
    await user.type(screen.getByLabelText('Key'), '  disk-srv  ');
    expect(screen.getByLabelText('Key')).toHaveValue('disk-srv');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(puts).toBe(1));
    expect(sent).toMatchObject({ key: 'disk-srv' });
  });

  // Whatever the form lets through, the server is the authority on a key —
  // so its refusal has to reach the operator rather than the dialog closing
  // on a change that did not happen.
  it("shows the server's refusal of a key and keeps the dialog open", async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ id: 'c7', key: 'disk-data', plugin: 'disk', origin: 'custom' })] },
      'PUT /api/v1/orgs/acme/checks/c7': () =>
        new Response(JSON.stringify({ detail: 'a check with this key already exists at this scope' }), {
          status: 409,
          headers: { 'Content-Type': 'application/problem+json' },
        }),
    });
    renderPage();

    await chooseFromMenu(user, 'disk-data (Organization)', 'Edit');
    await user.clear(screen.getByLabelText('Key'));
    await user.type(screen.getByLabelText('Key'), 'disk');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('a check with this key already exists at this scope');
    // Still open on the row it was editing — nothing was renamed.
    expect(screen.getByRole('dialog', { name: 'Edit disk-data' })).toBeInTheDocument();
  });

  // Review finding 5: the guard used to replace the whole page, taking
  // the only link to the section's other page with it.
  it('keeps the section tabs when the definitions list fails', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': () => new Response('{"detail":"boom"}', { status: 500, headers: { 'Content-Type': 'application/json' } }),
    });
    renderPage();

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Monitoring groups' })).toBeInTheDocument();
  });

  // Task 7 (v0.2f decision 2): a shared definition's key, plugin, interval
  // and params are read-only here (the installation's own), but its binding
  // and enabled flag are the OPPOSITE of a repository/job template's own
  // split — this organization's to edit, every time, regardless of origin.
  // Asserting only the disabled attributes would have missed the exact
  // regression Task 6's own report named (a guard that blocks Save on
  // exactly the fields a customer is supposed to be able to send), so this
  // actually clicks Save and asserts the PUT body reaches the server.
  it('keeps a shared definition fully read-only on its own fields while letting the organization rebind and disable it', async () => {
    const user = userEvent.setup();
    let put: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': {
        checks: [
          check({
            id: 'c10', key: 'disk-data', plugin: 'disk', interval_seconds: 300,
            origin: 'shared', shared_definition_id: 'sd1', binding_kind: 'org',
          }),
        ],
      },
      'PUT /api/v1/orgs/acme/checks/c10': (req: MockRequest) => {
        put = req.body as Record<string, unknown>;
        return { check: check({ id: 'c10' }) };
      },
    });
    renderPage();

    await chooseFromMenu(user, 'disk-data (Organization)', 'Edit');
    expect(screen.getByLabelText('Key')).toBeDisabled();
    expect(screen.getByLabelText('Plugin')).toBeDisabled();
    expect(screen.getByLabelText('Interval (seconds)')).toBeDisabled();
    expect(screen.getByText(/managed by the installation/)).toBeInTheDocument();

    // Rebinding to a group and disabling it — the organization's own,
    // always-editable half (spec decision 2's opposite split for checks).
    await pickOption(screen, 'Binds to', { value: 'group' });
    await user.click(screen.getByLabelText('Enabled'));
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(put).toEqual({
        plugin: 'disk', key: 'disk-data', params: {}, interval_seconds: 300, enabled: false,
        binding_kind: 'group', group_id: 'g1',
      }),
    );
  });

  it('copies a seeded definition into a custom one whose key can change', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': { checks: [check({ plugin: 'service', key: 'service', params: { units: ['sshd'] } })] },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { check: check({ id: 'c9', origin: 'custom' }) };
      },
    });
    renderPage();

    await chooseFromMenu(user, 'service (Organization)', 'Copy');
    // The copy is a create: the key is editable, and the plugin and its
    // parameters come across unchanged.
    expect(screen.getByLabelText('Key')).not.toBeDisabled();
    expect(screen.getByLabelText('Units (one per line)')).toHaveValue('sshd');
    await user.clear(screen.getByLabelText('Key'));
    await user.type(screen.getByLabelText('Key'), 'service-app');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toMatchObject({ key: 'service-app', plugin: 'service', params: { units: ['sshd'] }, binding_kind: 'org' }),
    );
  });

  // Task 7 (v0.2f): Copy is new for a SHARED row (seeded rows already had
  // it) — this is the create path, so none of the shared-only disabling
  // applies: key, plugin, interval and params all become editable, exactly
  // like copying a seeded row already does.
  it('copies a shared definition into a custom one whose key and interval can change', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/monitoring-groups': GROUPS,
      'GET /api/v1/orgs/acme/checks': {
        checks: [check({ id: 'c11', key: 'disk-data', plugin: 'disk', interval_seconds: 300, origin: 'shared', shared_definition_id: 'sd1' })],
      },
      'POST /api/v1/orgs/acme/checks': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { check: check({ id: 'c12', origin: 'custom' }) };
      },
    });
    renderPage();

    await chooseFromMenu(user, 'disk-data (Organization)', 'Copy');
    expect(screen.getByLabelText('Key')).not.toBeDisabled();
    expect(screen.getByLabelText('Plugin')).not.toBeDisabled();
    expect(screen.getByLabelText('Interval (seconds)')).not.toBeDisabled();
    await user.clear(screen.getByLabelText('Key'));
    await user.type(screen.getByLabelText('Key'), 'disk-data-copy');
    await user.clear(screen.getByLabelText('Interval (seconds)'));
    await user.type(screen.getByLabelText('Interval (seconds)'), '120');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(posted).toMatchObject({ key: 'disk-data-copy', plugin: 'disk', interval_seconds: 120, binding_kind: 'org' }),
    );
  });
});
