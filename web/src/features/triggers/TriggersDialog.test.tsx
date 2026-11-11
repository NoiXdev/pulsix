import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { TriggersDialog } from './TriggersDialog.tsx';
import { settled } from '../../test/settled.ts';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { acceptConfirm, declineConfirm } from '../../test/confirmDialog.ts';

const trigger = (over: Record<string, unknown> = {}) => ({
  id: 'tr1', kind: 'template_run', template_id: 't1', name: 'deploy on push', enabled: true, preset: 'github', auth: 'hmac',
  signature_header: 'X-Hub-Signature-256', allow_cidrs: ['140.82.112.0/20'], mapping: { extra_vars: { release: '$.ref' }, limit: '$.hosts[*]' },
  on_active: 'queue', rate_limit: { count: 10, per: '1m' }, url: 'https://pulsix.example.test/api/v1/hooks/jobs/tr1', revision: 1,
  created_at: '', updated_at: '', ...over,
});

function renderDialog(canWrite = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <TriggersDialog org="acme" target={{ kind: 'template_run', id: 't1', name: 'patch-all' }} canWrite={canWrite} canRun onClose={() => {}} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

describe('TriggersDialog', () => {
  it('shows URL, auth, allowlist, mapping and rate limit', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/triggers': { triggers: [trigger()] }, 'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] } });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Triggers · patch-all' });
    expect(await within(dialog).findByText('https://pulsix.example.test/api/v1/hooks/jobs/tr1')).toBeInTheDocument();
    expect(within(dialog).getByText('Auth: GitHub · X-Hub-Signature-256')).toBeInTheDocument();
    expect(within(dialog).getByText('140.82.112.0/20')).toBeInTheDocument();
    expect(within(dialog).getByText('$.ref → release')).toBeInTheDocument();
    expect(within(dialog).getByText('$.hosts[*] → limit')).toBeInTheDocument();
    expect(within(dialog).getByText('10 per 1m · queue if a run is active')).toBeInTheDocument();
  });

  it('creates a trigger and shows the secret exactly once', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    const list: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/triggers': () => ({ triggers: list }),
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'POST /api/v1/orgs/acme/triggers': (req: MockRequest) => {
        posted = req.body;
        list.push(trigger());
        return { trigger: trigger(), secret: 'whsec-shown-once', url: trigger().url };
      },
    });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'New trigger' }));
    await user.type(screen.getByLabelText('Name'), 'deploy on push');
    await pickOption(screen, 'Sender', { value: 'github' });
    await user.type(screen.getByLabelText('Allowed sources'), '140.82.112.0/20');
    await user.click(screen.getByRole('button', { name: 'Create trigger' }));
    expect(await screen.findByText('whsec-shown-once')).toBeInTheDocument();
    expect(posted).toMatchObject({ kind: 'template_run', template_id: 't1', name: 'deploy on push', preset: 'github', allow_cidrs: ['140.82.112.0/20'] });
    await user.click(screen.getByRole('button', { name: 'I stored the secret' }));
    expect(screen.queryByText('whsec-shown-once')).not.toBeInTheDocument();
  });

  it('rotates the secret, runs a test call and lists deliveries', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger()] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [{ id: 'd1', at: '2026-09-18T10:00:00Z', source_ip: '140.82.112.5', outcome: 'rejected_auth' }] },
      'POST /api/v1/orgs/acme/triggers/tr1/rotate-secret': { trigger: trigger({ revision: 2 }), secret: 'rotated-secret', url: trigger().url },
      'POST /api/v1/orgs/acme/triggers/tr1/test': { outcome: 'triggered', run_id: 'job-1' },
    });
    renderDialog();
    expect(await screen.findByText('rejected_auth')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Rotate secret' }));
    await acceptConfirm(user);
    expect(await screen.findByText('rotated-secret')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Test call' }));
    await acceptConfirm(user);
    expect(await screen.findByText(/Test call: triggered/)).toBeInTheDocument();
  });

  it('disables Delete while it runs and shows a toast when it fails', async () => {
    const user = userEvent.setup();
    let release: () => void = () => {};
    let calls = 0;
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger()] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'DELETE /api/v1/orgs/acme/triggers/tr1': async () => {
        calls++;
        await new Promise<void>((r) => (release = r));
        return new Response(JSON.stringify({ detail: 'delete refused' }), { status: 409 });
      },
    });
    renderDialog();
    const dialog = await screen.findByRole('dialog', { name: 'Triggers · patch-all' });
    const remove = await within(dialog).findByRole('button', { name: 'Delete' });
    await user.click(remove);
    await acceptConfirm(user);
    await waitFor(() => expect(remove).toBeDisabled());
    release();
    expect(await screen.findByText('delete refused')).toBeInTheDocument();
    expect(remove).toBeEnabled();
    expect(calls).toBe(1);
  });

  it('is read-only without write permission', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/triggers': { triggers: [trigger()] }, 'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] } });
    renderDialog(false);
    await screen.findByText('deploy on push');
    expect(screen.queryByRole('button', { name: 'New trigger' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Rotate secret' })).not.toBeInTheDocument();
  });

  // Fix round 1, Important 2: global-constraints.md's "A test call enqueues
  // a real run, and the button says so" was dropped from the UI. Test call
  // now confirms before sending, mirroring Rotate secret's confirmation
  // pattern — this pins the warning text and that declining the confirm
  // makes no request at all.
  it('confirms before a test call, stating it starts a real run', async () => {
    const user = userEvent.setup();
    let testCalls = 0;
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger()] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'POST /api/v1/orgs/acme/triggers/tr1/test': () => {
        testCalls += 1;
        return { outcome: 'triggered' };
      },
    });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'Test call' }));
    expect(await declineConfirm(user)).toMatch(/real/i);
    await settled();
    expect(testCalls).toBe(0);

    await user.click(screen.getByRole('button', { name: 'Test call' }));
    await acceptConfirm(user);
    expect(await screen.findByText(/Test call: triggered/)).toBeInTheDocument();
    expect(testCalls).toBe(1);
  });

  // Fix round 1, Minor 2: TriggersDialog.test.tsx never rendered with a
  // repo_sync target, leaving TriggerForm's mapping-fieldset-hidden branch
  // and TriggerCard's `(canRun || t.kind === 'repo_sync')` Test-call
  // visibility branch untested.
  it('supports a repo_sync target: hides the payload mapping and always shows Test call', async () => {
    const user = userEvent.setup();
    const repoTrigger = trigger({ id: 'tr2', kind: 'repo_sync', template_id: undefined, repo_id: 'r1', mapping: {} });
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [repoTrigger] },
      'GET /api/v1/orgs/acme/triggers/tr2/deliveries': { deliveries: [] },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <ConfirmProvider>
          <ToastProvider>
            <TriggersDialog org="acme" target={{ kind: 'repo_sync', id: 'r1', name: 'infra' }} canWrite canRun={false} onClose={() => {}} />
          </ToastProvider>
        </ConfirmProvider>
      </QueryClientProvider>,
    );
    // canRun is false, but the trigger's own kind is repo_sync: Test call
    // must still show (TriggerCard checks the trigger's kind, not canRun).
    expect(await screen.findByRole('button', { name: 'Test call' })).toBeInTheDocument();
    // A repo_sync target's create form has no payload-mapping fieldset.
    await user.click(screen.getByRole('button', { name: 'New trigger' }));
    expect(screen.queryByText('Payload mapping')).not.toBeInTheDocument();
  });

  // Fix round 1, Minor 1: TriggerForm sat at 60% statement coverage because
  // only the create path was exercised — the edit path (pre-filled fields,
  // If-Match carrying the loaded revision) had none.
  it('edits a trigger, sending the loaded revision as If-Match', async () => {
    const user = userEvent.setup();
    let putIfMatch: string | null = null;
    let putBody: unknown;
    let current = trigger();
    mockApi({
      'GET /api/v1/orgs/acme/triggers': () => ({ triggers: [current] }),
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'PUT /api/v1/orgs/acme/triggers/tr1': (req: MockRequest) => {
        putIfMatch = req.headers.get('If-Match');
        putBody = req.body;
        current = trigger({ name: 'deploy on release', revision: 2 });
        return { trigger: current };
      },
    });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'Edit' }));
    const nameInput = screen.getByLabelText('Name');
    await user.clear(nameInput);
    await user.type(nameInput, 'deploy on release');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('deploy on release')).toBeInTheDocument();
    expect(putIfMatch).toBe('"1"');
    expect(putBody).toMatchObject({ name: 'deploy on release' });
  });
});
