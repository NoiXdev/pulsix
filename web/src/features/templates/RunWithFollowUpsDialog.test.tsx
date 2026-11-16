import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import type { Template } from '../../api/types.ts';
import { RunWithFollowUpsDialog } from './RunWithFollowUpsDialog.tsx';

const base = { playbook_id: 'pb1', scope: {}, limit: '', extra_vars: {}, revision: 1, created_at: '', updated_at: '' };
const tpl = (id: string, name: string, over: Record<string, unknown> = {}) => ({ id, name, ...base, follow_ups: [], ...over }) as unknown as Template;

const A = tpl('a', 'alpha', { follow_ups: [{ template_id: 'b', template_name: 'beta', position: 0 }] });

function renderDialog(template: Template, onStarted: (jobs: { id: string }[]) => void = () => undefined, onClose = () => undefined) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RunWithFollowUpsDialog org="acme" template={template} onStarted={onStarted} onClose={onClose} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

function setup() {
  const posts: MockRequest[] = [];
  mockApi({
    'GET /api/v1/orgs/acme/job-templates': { templates: [A, tpl('b', 'beta'), tpl('c', 'gamma')] },
    'POST /api/v1/orgs/acme/job-templates/a/run': (req: MockRequest) => {
      posts.push(req);
      return new Response(JSON.stringify({ jobs: [{ id: 'job-1' }] }), { status: 202, headers: { 'Content-Type': 'application/json' } });
    },
  });
  return posts;
}

describe('RunWithFollowUpsDialog', () => {
  it('names the template, preselects its follow-ups and sends none when unchanged', async () => {
    const user = userEvent.setup();
    const posts = setup();
    let started: { id: string }[] = [];
    renderDialog(A, (jobs) => { started = jobs; });
    const dialog = await screen.findByRole('dialog', { name: 'Run alpha' });
    await within(dialog).findByText('beta');
    const list = within(dialog).getByRole('list', { name: 'Chosen follow-ups' });
    expect(within(list).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['beta']);
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body ?? null).toBeNull();
    expect(started).toEqual([{ id: 'job-1' }]);
  });

  it('sends an empty list when every follow-up is deselected', async () => {
    const user = userEvent.setup();
    const posts = setup();
    renderDialog(A);
    const dialog = await screen.findByRole('dialog', { name: 'Run alpha' });
    await user.click(await within(dialog).findByRole('button', { name: 'Remove beta' }));
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body).toEqual({ follow_ups: [] });
  });

  it('sends the whole list, in order, when one is added', async () => {
    const user = userEvent.setup();
    const posts = setup();
    renderDialog(A);
    const dialog = await screen.findByRole('dialog', { name: 'Run alpha' });
    await within(dialog).findByText('beta');
    await pickOption(within(dialog), 'Follow-ups on success', 'gamma');
    await user.click(within(dialog).getByRole('button', { name: 'Run' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body).toEqual({ follow_ups: ['b', 'c'] });
  });
});
