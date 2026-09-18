import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { TriggersDialog } from './TriggersDialog.tsx';

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
      <ToastProvider>
        <TriggersDialog org="acme" target={{ kind: 'template_run', id: 't1', name: 'patch-all' }} canWrite={canWrite} canRun onClose={() => {}} />
      </ToastProvider>
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
    await user.selectOptions(screen.getByLabelText('Sender'), 'github');
    await user.type(screen.getByLabelText('Allowed sources'), '140.82.112.0/20');
    await user.click(screen.getByRole('button', { name: 'Create trigger' }));
    expect(await screen.findByText('whsec-shown-once')).toBeInTheDocument();
    expect(posted).toMatchObject({ kind: 'template_run', template_id: 't1', name: 'deploy on push', preset: 'github', allow_cidrs: ['140.82.112.0/20'] });
    await user.click(screen.getByRole('button', { name: 'I stored the secret' }));
    expect(screen.queryByText('whsec-shown-once')).not.toBeInTheDocument();
  });

  it('rotates the secret, runs a test call and lists deliveries', async () => {
    const user = userEvent.setup();
    window.confirm = () => true;
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger()] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [{ id: 'd1', at: '2026-09-18T10:00:00Z', source_ip: '140.82.112.5', outcome: 'rejected_auth' }] },
      'POST /api/v1/orgs/acme/triggers/tr1/rotate-secret': { trigger: trigger({ revision: 2 }), secret: 'rotated-secret', url: trigger().url },
      'POST /api/v1/orgs/acme/triggers/tr1/test': { outcome: 'triggered', run_id: 'job-1' },
    });
    renderDialog();
    expect(await screen.findByText('rejected_auth')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Rotate secret' }));
    expect(await screen.findByText('rotated-secret')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Test call' }));
    expect(await screen.findByText(/Test call: triggered/)).toBeInTheDocument();
  });

  it('is read-only without write permission', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/triggers': { triggers: [trigger()] }, 'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] } });
    renderDialog(false);
    await screen.findByText('deploy on push');
    expect(screen.queryByRole('button', { name: 'New trigger' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Rotate secret' })).not.toBeInTheDocument();
  });
});
