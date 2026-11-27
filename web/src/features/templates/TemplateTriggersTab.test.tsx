import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { acceptConfirm } from '../../test/confirmDialog.ts';
import { pageRoutes, renderTemplatePage, trigger } from './templatePageTestKit.tsx';

const TRIGGERS = '/o/acme/templates/t1?tab=triggers';
const card = async (name: string) => within(await screen.findByRole('article', { name }));
/** The value a card's fact list gives for a label. */
const fact = (c: ReturnType<typeof within>, label: string) => c.getByText(label, { selector: 'dt' }).nextElementSibling;

const secretBody = (id: string) => ({ secret: 'pxs_new_secret', url: `https://pulsix.example/hooks/${id}`, trigger: trigger(id) });

describe('TemplateTriggersTab', () => {
  it('shows one card per trigger with URL, sender, timestamp header, sources, on-active and state', async () => {
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/triggers': {
            triggers: [
              trigger('w1', { timestamp_header: 'X-Pulsix-Timestamp', allow_cidrs: ['10.0.0.0/8', '192.168.1.0/24'], on_active: 'skip' }),
              trigger('w2', { enabled: false, preset: 'bearer' }),
              trigger('w3', { enabled: false, disabled_reason: 'assistant_change', review_request_id: 'rv1' }),
            ],
          },
        },
      }),
    );
    renderTemplatePage(TRIGGERS);
    const w1 = await card('push-w1');
    expect(fact(w1, 'URL')).toHaveTextContent('https://pulsix.example/hooks/w1');
    expect(fact(w1, 'Sender')).toHaveTextContent('GitHub');
    expect(fact(w1, 'Timestamp header')).toHaveTextContent('X-Pulsix-Timestamp');
    expect(fact(w1, 'Allowed sources')).toHaveTextContent('2 address ranges');
    expect(fact(w1, 'While a run is active')).toHaveTextContent('skip');
    expect(w1.getByText('enabled')).toBeInTheDocument();

    const w2 = await card('push-w2');
    expect(fact(w2, 'Sender')).toHaveTextContent('Bearer token');
    expect(fact(w2, 'Timestamp header')).toHaveTextContent('none');
    expect(fact(w2, 'Allowed sources')).toHaveTextContent('any');
    expect(fact(w2, 'While a run is active')).toHaveTextContent('queue another run');
    expect(w2.getByText('disabled')).toBeInTheDocument();

    const w3 = await card('push-w3');
    expect(w3.getByText('needs review')).toBeInTheDocument();
    expect(w3.getByRole('link', { name: 'Open the review' })).toHaveAttribute('href', '/o/acme/runs/requests/rv1');
  });

  it('says when no trigger starts the template', async () => {
    mockApi(pageRoutes());
    renderTemplatePage(TRIGGERS);
    expect(await screen.findByText('No webhook trigger starts this template yet.')).toBeInTheDocument();
  });

  it('creates a trigger for this template and shows its secret once', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi(pageRoutes({ over: { 'POST /api/v1/orgs/acme/triggers': (req: MockRequest) => ((posted = req.body), secretBody('w9')) } }));
    renderTemplatePage(TRIGGERS);
    await user.click(await screen.findByRole('button', { name: 'New trigger' }));
    await user.type(screen.getByLabelText('Name'), 'on-release');
    await user.click(screen.getByRole('button', { name: 'Create trigger' }));
    await waitFor(() => expect(posted).toMatchObject({ kind: 'template_run', template_id: 't1', name: 'on-release' }));
    const dialog = within(await screen.findByRole('dialog', { name: "The trigger's secret" }));
    expect(dialog.getByText('pxs_new_secret')).toBeInTheDocument();
    expect(dialog.getByText('https://pulsix.example/hooks/w9')).toBeInTheDocument();
    await user.click(dialog.getByRole('button', { name: 'I stored the secret' }));
    expect(screen.queryByText('pxs_new_secret')).not.toBeInTheDocument();
  });

  it('gives a trigger a new secret after asking, and shows it once', async () => {
    const user = userEvent.setup();
    const rotated: string[] = [];
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1')] },
          'POST /api/v1/orgs/acme/triggers/w1/rotate-secret': (req: MockRequest) => (rotated.push(req.path), secretBody('w1')),
        },
      }),
    );
    renderTemplatePage(TRIGGERS);
    await user.click((await card('push-w1')).getByRole('button', { name: 'New secret' }));
    expect(await acceptConfirm(user)).toMatch(/Rotate the secret of push-w1/);
    const dialog = within(await screen.findByRole('dialog', { name: "The trigger's secret" }));
    expect(dialog.getByText('pxs_new_secret')).toBeInTheDocument();
    expect(rotated).toEqual(['/api/v1/orgs/acme/triggers/w1/rotate-secret']);
  });

  it('edits a trigger in its card under If-Match', async () => {
    const user = userEvent.setup();
    let put: MockRequest | undefined;
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1', { revision: 4 })] },
          'PUT /api/v1/orgs/acme/triggers/w1': (req: MockRequest) => ((put = req), { trigger: trigger('w1', { revision: 5 }) }),
        },
      }),
    );
    renderTemplatePage(TRIGGERS);
    const c = await card('push-w1');
    await user.click(c.getByRole('button', { name: 'Edit' }));
    const name = c.getByLabelText('Name');
    await user.clear(name);
    await user.type(name, 'push-main');
    await user.click(c.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put?.headers.get('If-Match')).toBe('"4"'));
    expect(put?.body).toMatchObject({ name: 'push-main', template_id: 't1' });
    await waitFor(() => expect(c.queryByLabelText('Name')).not.toBeInTheDocument());
  });

  it("shows and hides a trigger's deliveries", async () => {
    const user = userEvent.setup();
    mockApi(
      pageRoutes({
        role: 'viewer',
        over: {
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1')] },
          'GET /api/v1/orgs/acme/triggers/w1/deliveries': { deliveries: [{ id: 'd1', at: '2026-11-26T08:00:00Z', outcome: 'triggered', source_ip: '10.1.2.3' }] },
        },
      }),
    );
    renderTemplatePage(TRIGGERS);
    const c = await card('push-w1');
    await user.click(c.getByRole('button', { name: 'Deliveries' }));
    const log = within(c.getByRole('region', { name: 'Deliveries of push-w1' }));
    expect(await log.findByText('10.1.2.3')).toBeInTheDocument();
    await user.click(c.getByRole('button', { name: 'Hide deliveries' }));
    expect(c.queryByRole('region', { name: 'Deliveries of push-w1' })).not.toBeInTheDocument();
  });

  it('offers a viewer no change', async () => {
    mockApi(pageRoutes({ role: 'viewer', over: { 'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1')] } } }));
    renderTemplatePage(TRIGGERS);
    const c = await card('push-w1');
    for (const name of ['Edit', 'New secret', 'Delete']) expect(c.queryByRole('button', { name })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New trigger' })).not.toBeInTheDocument();
    expect(c.getByRole('button', { name: 'Deliveries' })).toBeInTheDocument();
  });

  it('deletes a trigger after asking', async () => {
    const user = userEvent.setup();
    let deleted = false;
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1')] },
          'DELETE /api/v1/orgs/acme/triggers/w1': () => ((deleted = true), new Response(null, { status: 204 })),
        },
      }),
    );
    renderTemplatePage(TRIGGERS);
    await user.click((await card('push-w1')).getByRole('button', { name: 'Delete' }));
    expect(await acceptConfirm(user)).toMatch(/Delete trigger push-w1/);
    await waitFor(() => expect(deleted).toBe(true));
  });

  // Backlog 2.137: the settle delay is a fact of the card, and a waiting
  // collected run shows with Run now (jobs.run) and Discard; a viewer sees
  // it without either.
  it("shows a trigger's settle delay and its waiting collected run", async () => {
    const user = userEvent.setup();
    let ran = 0;
    const pending = { id: 'p1', first_at: '2026-11-26T10:00:00Z', last_at: '2026-11-26T10:00:20Z', due_at: '2026-11-26T10:00:50Z', deadline_at: '2026-11-26T10:10:00Z', deliveries: 2 };
    mockApi(
      pageRoutes({
        over: {
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1', { settle_seconds: 60, max_wait_seconds: 3600 }), trigger('w2')] },
          'GET /api/v1/orgs/acme/triggers/w1/pending': () => (ran ? {} : { pending }),
          'POST /api/v1/orgs/acme/triggers/w1/pending/run': () => (ran++, { outcome: 'triggered', run_id: 'j1' }),
        },
      }),
    );
    renderTemplatePage(TRIGGERS);
    const w1 = await card('push-w1');
    expect(fact(w1, 'Wait for a quiet moment')).toHaveTextContent('1 min after the last call, at the latest 1 h after the first');
    expect(fact(w1, 'While a run is active')).toHaveTextContent('waits for it, then starts once');
    expect(fact(await card('push-w2'), 'Wait for a quiet moment')).toHaveTextContent('off – every call starts a run');
    const box = within(await w1.findByRole('region', { name: 'A collected run is waiting' }));
    expect(box.getByText('2 deliveries collected')).toBeInTheDocument();
    expect(box.getByRole('button', { name: 'Discard' })).toBeInTheDocument();
    await user.click(box.getByRole('button', { name: 'Run now' }));
    await waitFor(() => expect(ran).toBe(1));
    await waitFor(() => expect(w1.queryByRole('region', { name: 'A collected run is waiting' })).not.toBeInTheDocument());
  });

  it('shows a viewer the waiting collected run without Run now or Discard', async () => {
    const pending = { id: 'p1', first_at: '2026-11-26T10:00:00Z', last_at: '2026-11-26T10:00:20Z', due_at: '2026-11-26T10:00:50Z', deadline_at: '2026-11-26T10:10:00Z', deliveries: 1 };
    mockApi(
      pageRoutes({
        role: 'viewer',
        over: {
          'GET /api/v1/orgs/acme/triggers': { triggers: [trigger('w1', { settle_seconds: 30 })] },
          'GET /api/v1/orgs/acme/triggers/w1/pending': { pending },
        },
      }),
    );
    renderTemplatePage(TRIGGERS);
    const box = within(await (await card('push-w1')).findByRole('region', { name: 'A collected run is waiting' }));
    expect(box.getByText('1 delivery collected')).toBeInTheDocument();
    expect(box.queryByRole('button', { name: 'Run now' })).not.toBeInTheDocument();
    expect(box.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument();
  });
});
