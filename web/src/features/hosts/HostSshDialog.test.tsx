import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useHost } from '../../api/hooks/useHosts.ts';
import { mockApi, type MockRequest, type MockRoute } from '../../test/mockApi.ts';
import { HOST, hostRoutes, renderAt } from '../../test/hostDetail.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { HostSshDialog } from './HostSshDialog.tsx';

const KEY = {
  id: 'c1', name: 'inv-deploy', kind: 'ssh_key', username: 'ansible', public_key: 'ssh-ed25519 AAAA pulsix:acme:inv-deploy',
  expired: false, expires_soon: false, used_by: { bastions: 0, hosts: 1 }, created_at: '', updated_at: '',
};

const SSH = { host_id: 'h1', username: 'ops', port: 2222, credential_id: 'c1', bastion_mode: 'inherit' };

const TEST_RESULT = (at: string, ok: boolean) => ({
  test: { at, ok, host_key: 'SHA256:abc', host_key_state: 'unpinned', checks: [{ name: 'tcp', detail: 'connected (via bastion-a)', ok }] },
});

function routes(extra: Record<string, MockRoute> = {}, role = 'admin') {
  return hostRoutes(
    { host: { managed: true }, role },
    {
      'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: SSH },
      'GET /api/v1/orgs/acme/bastions': {
        bastions: [
          { id: 'b1', name: 'bastion-a', address: 'a.example', port: 22, username: 'ops' },
          { id: 'b2', name: 'bastion-b', address: 'b.example', port: 2222, username: 'ops', via_bastion_id: 'b1' },
        ],
      },
      'GET /api/v1/orgs/acme/credentials': { credentials: [KEY] },
      'GET /api/v1/orgs/acme/credentials/c1': { credential: KEY, snippets: { authorized_keys_command: 'echo', cloud_init: '#cloud-config', terraform: 'locals {}' } },
      'GET /api/v1/orgs/acme/hosts/h1/inventory/preview': {
        vars: {},
        connection: { host_id: 'h1', bastion_source: 'group:production', bastions: ['b1', 'b2'] },
        warnings: [],
      },
      'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', name: 'Frankfurt', slug: 'fra-1', bastion_id: 'b1' }] },
      'GET /api/v1/orgs/acme/hosts/h1/connection-test': TEST_RESULT('2026-11-01T10:00:00Z', false),
      ...extra,
    },
  );
}

/** The dialog over a page that holds the host the way HostDetailPage does
 * (live from its query), saying when the dialog closed. */
function renderDialog({ role = 'admin', snippets = false } = {}) {
  function Page() {
    const host = useHost('acme', 'h1');
    const [open, setOpen] = useState(true);
    if (!host.data) return null;
    return (
      <>
        {!open && <p>CLOSED</p>}
        <HostSshDialog org="acme" host={host.data.host} role={role} open={open} showSnippets={snippets} onClose={() => setOpen(false)} />
      </>
    );
  }
  return renderAt(Page);
}

const dialog = async () => within(await screen.findByRole('dialog', { name: 'SSH access · web-04.fra' }));
const save = (d: ReturnType<typeof within>) => d.getByRole('button', { name: 'Save' });

describe('HostSshDialog', () => {
  it('opens prefilled from the host and its SSH settings, with nothing to save', async () => {
    mockApi(routes());
    renderDialog();
    const d = await dialog();

    expect(await d.findByLabelText('Address')).toHaveValue('203.0.113.4');
    expect(d.getByLabelText('Port')).toHaveValue('2222');
    expect(d.getByLabelText('User')).toHaveValue('ops');
    await waitFor(() => expect(d.getByLabelText('Credential')).toHaveValue('inv-deploy'));
    expect(d.getByRole('radio', { name: 'Inherit' })).toBeChecked();
    expect(await d.findByText('Now: inherited from group production · bastion-a → bastion-b')).toBeInTheDocument();
    expect(d.getByLabelText('Route preview')).toHaveTextContent('worker (fra-1)→bastion-a→bastion-b→ops@203.0.113.4:2222');
    expect(save(d)).toBeDisabled();
    expect(d.queryByText('Unsaved changes')).not.toBeInTheDocument();
  });

  it('is dirty while something differs from what is stored', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderDialog();
    const d = await dialog();

    const port = await d.findByLabelText('Port');
    await user.clear(port);
    await user.type(port, '22');
    expect(d.getByText('Unsaved changes')).toBeInTheDocument();
    expect(save(d)).toBeEnabled();

    await user.clear(port);
    await user.type(port, '2222');
    expect(d.queryByText('Unsaved changes')).not.toBeInTheDocument();
    expect(save(d)).toBeDisabled();
  });

  it('saves the address through the host update first, then the SSH settings, and closes', async () => {
    const user = userEvent.setup();
    const calls: Array<{ what: string; ifMatch: string | null; body: unknown }> = [];
    mockApi(
      routes({
        'PATCH /api/v1/orgs/acme/hosts/h1': (req: MockRequest) => {
          calls.push({ what: 'host', ifMatch: req.headers.get('If-Match'), body: req.body });
          return { host: { ...HOST, managed: true, address: 'web-04.internal', revision: 4 } };
        },
        'PUT /api/v1/orgs/acme/hosts/h1/ssh': (req: MockRequest) => {
          calls.push({ what: 'ssh', ifMatch: req.headers.get('If-Match'), body: req.body });
          return { ssh: { ...SSH, username: 'deploy' }, host_revision: 5 };
        },
      }),
    );
    renderDialog();
    const d = await dialog();

    const address = await d.findByLabelText('Address');
    await user.clear(address);
    await user.type(address, 'web-04.internal');
    await user.clear(d.getByLabelText('User'));
    await user.type(d.getByLabelText('User'), 'deploy');
    expect(d.getByLabelText('Route preview')).toHaveTextContent('deploy@web-04.internal:2222');
    await user.click(save(d));

    expect(await screen.findByText('CLOSED')).toBeInTheDocument();
    expect(await screen.findByText('SSH settings saved')).toBeInTheDocument();
    expect(calls).toEqual([
      { what: 'host', ifMatch: '"3"', body: { name: 'web-04.fra', zone_id: 'z1', address: 'web-04.internal', managed: true, monitored: false } },
      { what: 'ssh', ifMatch: '"4"', body: { username: 'deploy', port: 2222, credential_id: 'c1', bastion_mode: 'inherit' } },
    ]);
  });

  it('leaves the host alone when only the SSH settings changed', async () => {
    const user = userEvent.setup();
    let patched = false;
    mockApi(
      routes({
        'PATCH /api/v1/orgs/acme/hosts/h1': () => {
          patched = true;
          return {};
        },
        'PUT /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { ...SSH, port: 22 }, host_revision: 4 },
      }),
    );
    renderDialog();
    const d = await dialog();

    await user.clear(await d.findByLabelText('Port'));
    await user.type(d.getByLabelText('Port'), '22');
    await user.click(save(d));
    expect(await screen.findByText('CLOSED')).toBeInTheDocument();
    expect(patched).toBe(false);
  });

  it('stays open with the error when the SSH save fails after the address was saved', async () => {
    const user = userEvent.setup();
    let patched = 0;
    mockApi(
      routes({
        'PATCH /api/v1/orgs/acme/hosts/h1': () => {
          patched += 1;
          return { host: { ...HOST, managed: true, address: 'web-04.internal', revision: 4 } };
        },
        'PUT /api/v1/orgs/acme/hosts/h1/ssh': () =>
          new Response(JSON.stringify({ title: 'x', status: 422, detail: 'credential is not an SSH credential' }), {
            status: 422,
            headers: { 'Content-Type': 'application/problem+json' },
          }),
      }),
    );
    renderDialog();
    const d = await dialog();

    await user.clear(await d.findByLabelText('Address'));
    await user.type(d.getByLabelText('Address'), 'web-04.internal');
    await user.type(d.getByLabelText('User'), 'x');
    await user.click(save(d));

    expect(await d.findByRole('alert')).toHaveTextContent('Address saved; SSH settings not saved: credential is not an SSH credential');
    expect(screen.queryByText('CLOSED')).not.toBeInTheDocument();
    expect(patched).toBe(1);
    // The address is stored now; what is left to save is the SSH side.
    expect(d.getByText('Unsaved changes')).toBeInTheDocument();
  });

  it('offers the three jump host choices and previews the route each leads to', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi(
      routes({
        'PUT /api/v1/orgs/acme/hosts/h1/ssh': (req: MockRequest) => {
          put = req.body;
          return { ssh: { ...SSH, bastion_mode: 'explicit', bastion_id: 'b2' }, host_revision: 4 };
        },
      }),
    );
    renderDialog();
    const d = await dialog();
    const preview = () => d.getByLabelText('Route preview');
    await d.findByText('Now: inherited from group production · bastion-a → bastion-b');

    await user.click(d.getByRole('radio', { name: 'Direct' }));
    expect(preview()).toHaveTextContent(/^worker \(fra-1\)→ops@203\.0\.113\.4:2222$/);
    expect(d.getByText('direct connection')).toBeInTheDocument();

    await user.click(d.getByRole('radio', { name: 'Specific' }));
    await user.click(save(d));
    expect(d.getByText('Choose the jump host.')).toBeInTheDocument();
    expect(put).toBeUndefined();

    await pickOption(d, 'Jump host for this host', { value: 'b2' });
    expect(preview()).toHaveTextContent('worker (fra-1)→bastion-a→bastion-b→ops@203.0.113.4:2222');
    expect(d.getByText('set on this host')).toBeInTheDocument();
    await user.click(save(d));
    await screen.findByText('CLOSED');
    expect(put).toEqual({ username: 'ops', port: 2222, credential_id: 'c1', bastion_mode: 'explicit', bastion_id: 'b2' });
  });

  it('says what inheriting would give when the host does not inherit today', async () => {
    mockApi(routes({ 'GET /api/v1/orgs/acme/hosts/h1/ssh': { ssh: { ...SSH, bastion_mode: 'none' } } }));
    renderDialog();
    const d = await dialog();
    expect(await d.findByText('Zone default: bastion-a — an inventory group may override it')).toBeInTheDocument();
    expect(d.getByRole('radio', { name: 'Direct' })).toBeChecked();
  });

  it('keeps the key snippets folded until asked', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderDialog();
    const d = await dialog();

    const summary = await d.findByText('Install this key on the host');
    expect(summary.closest('details')).not.toHaveAttribute('open');
    expect(d.queryByLabelText('Public key')).not.toBeInTheDocument();

    await user.click(summary);
    expect(await d.findByLabelText('Public key')).toHaveTextContent('pulsix:acme:inv-deploy');
  });

  it('opens with the key snippets unfolded from the key snippets card', async () => {
    mockApi(routes());
    renderDialog({ snippets: true });
    const d = await dialog();
    expect(await d.findByLabelText('Public key')).toHaveTextContent('pulsix:acme:inv-deploy');
  });

  it('tests the stored values in place when nothing changed', async () => {
    const user = userEvent.setup();
    let started = false;
    mockApi(
      routes({
        'POST /api/v1/orgs/acme/hosts/h1/connection-test': () => {
          started = true;
          return { job_id: 'j1' };
        },
        'GET /api/v1/orgs/acme/jobs/j1': { job: { id: 'j1', kind: 'connection_test', status: 'succeeded', created_at: '2026-11-18T10:00:00Z' } },
        'GET /api/v1/orgs/acme/hosts/h1/connection-test': () =>
          started ? TEST_RESULT('2026-11-18T10:00:05Z', true) : TEST_RESULT('2026-11-01T10:00:00Z', false),
      }),
    );
    renderDialog();
    const d = await dialog();

    const button = await d.findByRole('button', { name: 'Test connection' });
    expect(button).toHaveAccessibleDescription('Checks the saved values from the worker.');
    await user.click(button);

    expect(await d.findByText('connected (via bastion-a)')).toBeInTheDocument();
    expect(d.getByText('ok')).toBeInTheDocument();
    expect(d.getByRole('link', { name: 'Open run' })).toHaveAttribute('href', '/o/acme/runs/j1');
    expect(screen.queryByText('CLOSED')).not.toBeInTheDocument();
  });

  it('says Queued while the run waits for a worker', async () => {
    const user = userEvent.setup();
    mockApi(
      routes({
        'POST /api/v1/orgs/acme/hosts/h1/connection-test': { job_id: 'j1' },
        'GET /api/v1/orgs/acme/jobs/j1': { job: { id: 'j1', kind: 'connection_test', status: 'queued', created_at: '2026-11-18T10:00:00Z' } },
      }),
    );
    renderDialog();
    const d = await dialog();

    await user.click(await d.findByRole('button', { name: 'Test connection' }));

    expect(await d.findByText('Queued…')).toBeInTheDocument();
    expect(d.queryByText('Testing…')).not.toBeInTheDocument();
  });

  it('says the run ended without a result, and shows no result from before it', async () => {
    const user = userEvent.setup();
    mockApi(
      routes({
        'POST /api/v1/orgs/acme/hosts/h1/connection-test': { job_id: 'j1' },
        'GET /api/v1/orgs/acme/jobs/j1': {
          job: { id: 'j1', kind: 'connection_test', status: 'failed', created_at: '2026-11-18T10:00:00Z', started_at: '2026-11-18T10:00:01Z' },
        },
        // The only result is the old one, recorded long before this run.
        'GET /api/v1/orgs/acme/hosts/h1/connection-test': TEST_RESULT('2026-11-01T10:00:00Z', false),
      }),
    );
    renderDialog();
    const d = await dialog();

    await user.click(await d.findByRole('button', { name: 'Test connection' }));

    expect(await d.findByText('The test ended (failed) without a result — the run says why.')).toBeInTheDocument();
    expect(d.queryByText('connected (via bastion-a)')).not.toBeInTheDocument();
  });

  it('saves first, then tests, while there are changes: Save and test', async () => {
    const user = userEvent.setup();
    const order: string[] = [];
    let stored = SSH;
    mockApi(
      routes({
        'GET /api/v1/orgs/acme/hosts/h1/ssh': () => ({ ssh: stored }),
        'PUT /api/v1/orgs/acme/hosts/h1/ssh': () => {
          order.push('save');
          stored = { ...SSH, port: 22 };
          return { ssh: stored, host_revision: 4 };
        },
        'POST /api/v1/orgs/acme/hosts/h1/connection-test': () => {
          order.push('test');
          return { job_id: 'j1' };
        },
        'GET /api/v1/orgs/acme/jobs/j1': { job: { id: 'j1', kind: 'connection_test', status: 'running', created_at: '2026-11-18T10:00:00Z' } },
      }),
    );
    renderDialog();
    const d = await dialog();

    await user.clear(await d.findByLabelText('Port'));
    await user.type(d.getByLabelText('Port'), '22');
    const button = d.getByRole('button', { name: 'Save and test' });
    expect(button).toHaveAccessibleDescription('Saves the values above, then checks them from the worker.');
    await user.click(button);

    expect(await d.findByText('Testing…')).toBeInTheDocument();
    expect(order).toEqual(['save', 'test']);
    expect(await screen.findByText('SSH settings saved')).toBeInTheDocument();
    // Saved: nothing left to save, the dialog stays for the result.
    await waitFor(() => expect(save(d)).toBeDisabled());
    expect(screen.queryByText('CLOSED')).not.toBeInTheDocument();
  });

  it('asks before Cancel throws changes away', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderDialog();
    const d = await dialog();

    await user.type(await d.findByLabelText('User'), 'x');
    await user.click(d.getByRole('button', { name: 'Cancel' }));
    let confirm = within(await screen.findByRole('dialog', { name: 'Discard changes?' }));
    await user.click(confirm.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByText('CLOSED')).not.toBeInTheDocument();

    await user.click(d.getByRole('button', { name: 'Cancel' }));
    confirm = within(await screen.findByRole('dialog', { name: 'Discard changes?' }));
    await user.click(confirm.getByRole('button', { name: 'Discard' }));
    expect(await screen.findByText('CLOSED')).toBeInTheDocument();
  });

  it('closes on Cancel at once when nothing changed', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderDialog();
    const d = await dialog();
    await d.findByLabelText('User');
    await user.click(d.getByRole('button', { name: 'Cancel' }));
    expect(await screen.findByText('CLOSED')).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Discard changes?' })).not.toBeInTheDocument();
  });

  it('offers to create a credential and a jump host only with credentials.write', async () => {
    const user = userEvent.setup();
    mockApi(routes({}, 'operator'));
    renderDialog({ role: 'operator' });
    const d = await dialog();

    await user.click(await d.findByLabelText('Credential'));
    expect(screen.queryByRole('option', { name: /New credential/ })).not.toBeInTheDocument();
    await user.keyboard('{Escape}');
    await user.click(d.getByRole('radio', { name: 'Specific' }));
    await user.click(d.getByLabelText('Jump host for this host'));
    expect(screen.queryByRole('option', { name: /New jump host/ })).not.toBeInTheDocument();
  });

  it('offers both create rows to an admin', async () => {
    const user = userEvent.setup();
    mockApi(routes());
    renderDialog();
    const d = await dialog();

    await user.click(await d.findByLabelText('Credential'));
    expect(screen.getByRole('option', { name: /New credential/ })).toBeInTheDocument();
    await user.keyboard('{Escape}');
    await user.click(d.getByRole('radio', { name: 'Specific' }));
    await user.click(d.getByLabelText('Jump host for this host'));
    expect(screen.getByRole('option', { name: /New jump host/ })).toBeInTheDocument();
  });
});
