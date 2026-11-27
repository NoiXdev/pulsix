import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { TriggersDialog } from './TriggersDialog.tsx';

/** A trigger's settle delay and its collected run (backlog 2.137). */

const trigger = (over: Record<string, unknown> = {}) => ({
  id: 'tr1', kind: 'template_run', template_id: 't1', name: 'deploy on push', enabled: true, preset: 'github', auth: 'hmac',
  signature_header: 'X-Hub-Signature-256', allow_cidrs: [], mapping: {}, on_active: 'queue', rate_limit: { count: 10, per: '1m' },
  url: 'https://pulsix.example.test/api/v1/hooks/jobs/tr1', revision: 1, settle_seconds: 0, created_at: '', updated_at: '', ...over,
});

const pending = { id: 'p1', first_at: '2026-11-26T10:00:00Z', last_at: '2026-11-26T10:00:20Z', due_at: '2026-11-26T10:00:50Z', deadline_at: '2026-11-26T10:10:00Z', deliveries: 3 };

function renderDialog({ canWrite = true, canRun = true } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <TriggersDialog org="acme" target={{ kind: 'template_run', id: 't1', name: 'patch-all' }} canWrite={canWrite} canRun={canRun} onClose={() => {}} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

describe('TriggerForm: the settle delay', () => {
  it('sends the wait and the latest run time on create', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [] },
      'POST /api/v1/orgs/acme/triggers': (req: MockRequest) => {
        posted = req.body;
        return { trigger: trigger(), secret: 's', url: trigger().url };
      },
    });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'New trigger' }));
    await user.type(screen.getByLabelText('Name'), 'bursty');
    expect(screen.queryByLabelText('Run at the latest after (seconds)')).not.toBeInTheDocument();
    await user.clear(screen.getByLabelText('Wait for a quiet moment (seconds)'));
    await user.type(screen.getByLabelText('Wait for a quiet moment (seconds)'), '30');
    expect(screen.getByLabelText('Run at the latest after (seconds)')).toHaveAttribute('placeholder', '300 (10 × the wait)');
    await user.type(screen.getByLabelText('Run at the latest after (seconds)'), '600');
    await user.click(screen.getByRole('button', { name: 'Create trigger' }));
    await waitFor(() => expect(posted).toMatchObject({ name: 'bursty', settle_seconds: 30, max_wait_seconds: 600 }));
  });

  it('refuses a wait or a latest run time the server would refuse', async () => {
    const user = userEvent.setup();
    let posts = 0;
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [] },
      'POST /api/v1/orgs/acme/triggers': () => {
        posts++;
        return { trigger: trigger(), secret: 's', url: trigger().url };
      },
    });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'New trigger' }));
    await user.type(screen.getByLabelText('Name'), 'bursty');
    const wait = screen.getByLabelText('Wait for a quiet moment (seconds)');
    await user.clear(wait);
    await user.type(wait, '3');
    await user.click(screen.getByRole('button', { name: 'Create trigger' }));
    expect(await screen.findByText('Wait 0 (off) or 5 to 3600 seconds.')).toBeInTheDocument();
    await user.clear(wait);
    await user.type(wait, '60');
    await user.type(screen.getByLabelText('Run at the latest after (seconds)'), '30');
    await user.click(screen.getByRole('button', { name: 'Create trigger' }));
    expect(await screen.findByText('Run at the latest after 60 to 86400 seconds, or leave it empty for 10 × the wait.')).toBeInTheDocument();
    expect(posts).toBe(0);
  });

  it('keeps the loaded settle delay on edit and can turn it off', async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger({ settle_seconds: 30, max_wait_seconds: 600 })] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'GET /api/v1/orgs/acme/triggers/tr1/pending': {},
      'PUT /api/v1/orgs/acme/triggers/tr1': (req: MockRequest) => {
        bodies.push(req.body);
        return { trigger: trigger() };
      },
    });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'Edit' }));
    expect(screen.getByLabelText('Wait for a quiet moment (seconds)')).toHaveValue('30');
    expect(screen.getByLabelText('Run at the latest after (seconds)')).toHaveValue('600');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(bodies[0]).toMatchObject({ settle_seconds: 30, max_wait_seconds: 600 }));
    await user.click(await screen.findByRole('button', { name: 'Edit' }));
    await user.clear(screen.getByLabelText('Wait for a quiet moment (seconds)'));
    await user.type(screen.getByLabelText('Wait for a quiet moment (seconds)'), '0');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(bodies[1]).toMatchObject({ settle_seconds: 0 }));
    expect(bodies[1]).not.toHaveProperty('max_wait_seconds');
  });
});

describe('TriggersDialog: a collected run', () => {
  it('shows the settle delay, the waiting run, and runs it now', async () => {
    const user = userEvent.setup();
    let ran = 0;
    let waiting: typeof pending | undefined = pending;
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger({ settle_seconds: 30 })] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'GET /api/v1/orgs/acme/triggers/tr1/pending': () => ({ pending: waiting }),
      'POST /api/v1/orgs/acme/triggers/tr1/pending/run': () => {
        ran++;
        waiting = undefined;
        return { outcome: 'triggered', run_id: 'job-1' };
      },
    });
    renderDialog();
    expect(await screen.findByText('Wait for a quiet moment: 30 s after the last call, at the latest 5 min after the first')).toBeInTheDocument();
    const box = within(await screen.findByRole('region', { name: 'A collected run is waiting' }));
    expect(box.getByText('3 deliveries collected')).toBeInTheDocument();
    expect(box.getByText(/^Runs at .* unless more arrive$/)).toBeInTheDocument();
    expect(box.getByText(/^At the latest at /)).toBeInTheDocument();
    await user.click(box.getByRole('button', { name: 'Run now' }));
    expect(await screen.findByText('The collected run started.')).toBeInTheDocument();
    expect(ran).toBe(1);
    await waitFor(() => expect(screen.queryByRole('region', { name: 'A collected run is waiting' })).not.toBeInTheDocument());
  });

  it('discards the waiting run after asking', async () => {
    const user = userEvent.setup();
    let discarded = 0;
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger({ settle_seconds: 30 })] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'GET /api/v1/orgs/acme/triggers/tr1/pending': () => (discarded ? {} : { pending }),
      'DELETE /api/v1/orgs/acme/triggers/tr1/pending': () => {
        discarded++;
        return {};
      },
    });
    renderDialog();
    const box = within(await screen.findByRole('region', { name: 'A collected run is waiting' }));
    await user.click(box.getByRole('button', { name: 'Discard' }));
    await acceptConfirm(user);
    expect(await screen.findByText('The collected run was discarded.')).toBeInTheDocument();
    expect(discarded).toBe(1);
  });

  it('offers neither button without write permission, and Run now only with run permission', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger({ settle_seconds: 30 })] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'GET /api/v1/orgs/acme/triggers/tr1/pending': { pending },
    });
    const view = renderDialog({ canWrite: false });
    let box = within(await screen.findByRole('region', { name: 'A collected run is waiting' }));
    expect(box.queryByRole('button', { name: 'Run now' })).not.toBeInTheDocument();
    expect(box.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument();
    view.unmount();
    renderDialog({ canRun: false });
    box = within(await screen.findByRole('region', { name: 'A collected run is waiting' }));
    expect(box.queryByRole('button', { name: 'Run now' })).not.toBeInTheDocument();
    expect(box.getByRole('button', { name: 'Discard' })).toBeInTheDocument();
  });

  it('says off for a trigger without a settle delay and reads no pending run', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger()] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
    });
    renderDialog();
    expect(await screen.findByText('Wait for a quiet moment: off – every call starts a run')).toBeInTheDocument();
  });
});

describe('Deliveries: collected deliveries', () => {
  it('says what became of each collected delivery', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger({ settle_seconds: 30 })] },
      'GET /api/v1/orgs/acme/triggers/tr1/pending': { pending },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': {
        deliveries: [
          { id: 'd3', at: '2026-11-26T10:00:20Z', outcome: 'collected', pending_id: 'p1' },
          { id: 'd2', at: '2026-11-26T09:00:00Z', outcome: 'collected', pending_id: 'p0', job_group_id: 'g1' },
          { id: 'd1', at: '2026-11-26T08:00:00Z', outcome: 'collected', pending_id: 'pX', reason: 'superseded by run g2', superseded_by: 'g2' },
        ],
      },
    });
    renderDialog();
    const waiting = await screen.findByText(/^Collected – runs at .* unless more arrive$/);
    expect(waiting.closest('li')).toHaveTextContent('collected');
    const collapsed = (await screen.findByText('Collapsed into run')).closest('li') as HTMLElement;
    expect(within(collapsed).getByRole('link', { name: 'g1' })).toBeInTheDocument();
    const superseded = screen.getByText('Superseded by run').closest('li') as HTMLElement;
    expect(within(superseded).getByRole('link', { name: 'g2' })).toBeInTheDocument();
    // The note the link already says is not repeated.
    expect(within(superseded).queryByText('superseded by run g2')).not.toBeInTheDocument();
  });
});

describe('final review: test calls, on_active and polling', () => {
  it('says a test call joins the waiting run, and when that run starts', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger({ settle_seconds: 30 })] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'GET /api/v1/orgs/acme/triggers/tr1/pending': {},
      'POST /api/v1/orgs/acme/triggers/tr1/test': { outcome: 'collected', due_at: '2026-11-26T10:00:50Z' },
    });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'Test call' }));
    expect(await acceptConfirm(user)).toMatch(/joins the waiting collected run and replaces its values/);
    expect(await screen.findByText(/^Test call: collected – runs at .* unless more arrive$/)).toBeInTheDocument();
  });

  it('says on_active does not apply while waiting for a quiet moment', async () => {
    const user = userEvent.setup();
    mockApi({ 'GET /api/v1/orgs/acme/triggers': { triggers: [] } });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'New trigger' }));
    const hint = 'Does not apply while waiting for a quiet moment: the collected run waits for an active run, then starts once.';
    expect(screen.queryByText(hint)).not.toBeInTheDocument();
    expect(screen.getByText(/A test call joins the waiting run too and replaces its values/)).toBeInTheDocument();
    const wait = screen.getByLabelText('Wait for a quiet moment (seconds)');
    await user.clear(wait);
    await user.type(wait, '30');
    expect(screen.getByText(hint)).toBeInTheDocument();
  });

  it('says a settling trigger waits for an active run on its card', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [trigger({ settle_seconds: 30 })] },
      'GET /api/v1/orgs/acme/triggers/tr1/deliveries': { deliveries: [] },
      'GET /api/v1/orgs/acme/triggers/tr1/pending': {},
    });
    renderDialog();
    expect(await screen.findByText('10 per 1m · wait, then start once if a run is active')).toBeInTheDocument();
  });

  it('accepts a latest run time of 0 as the default', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/triggers': { triggers: [] },
      'POST /api/v1/orgs/acme/triggers': (req: MockRequest) => {
        posted = req.body;
        return { trigger: trigger(), secret: 's', url: trigger().url };
      },
    });
    renderDialog();
    await user.click(await screen.findByRole('button', { name: 'New trigger' }));
    await user.type(screen.getByLabelText('Name'), 'bursty');
    const wait = screen.getByLabelText('Wait for a quiet moment (seconds)');
    await user.clear(wait);
    await user.type(wait, '30');
    await user.type(screen.getByLabelText('Run at the latest after (seconds)'), '0');
    await user.click(screen.getByRole('button', { name: 'Create trigger' }));
    await waitFor(() => expect(posted).toMatchObject({ settle_seconds: 30, max_wait_seconds: 0 }));
  });
});
