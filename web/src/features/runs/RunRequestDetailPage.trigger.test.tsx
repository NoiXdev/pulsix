import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs, request } from './runRequestTestKit.ts';

const TRIG = '0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a77';
const URL = `https://px.example.test/api/v1/hooks/jobs/${TRIG}`;
const SECRET = 'n3w-Tr1gger-S3cret-shown-once-0123456789abcd';

const current = {
  kind: 'template_run',
  template_id: 'tpl1',
  template_name: 'deploy-nginx',
  name: 'deploy hook',
  enabled: true,
  preset: 'github',
  signature_header: 'X-Hub-Signature-256',
  allow_cidrs: ['192.0.2.0/24'],
  mapping: { extra_vars: { release: '$.ref' } },
  on_active: 'queue',
  rate_limit: { count: 10, per: '1m' },
  revision: 4,
};

function triggerPicture(over: Record<string, unknown>, top: Record<string, unknown> = {}) {
  return {
    template_id: 'tpl1',
    template_name: 'deploy-nginx',
    template_revision: 7,
    limit: '',
    playbook: { id: '', source: '' },
    targets: [],
    vault_identities: [],
    extra_vars: {},
    vars_digest: '',
    repo: undefined,
    trigger: { trigger_id: TRIG, url: URL, ...over },
    ...top,
  };
}

function renderTrigger(
  trigger: Record<string, unknown>,
  role = 'operator',
  requestOver: Record<string, unknown> = {},
  extra: Record<string, unknown> = {},
  lang?: 'de',
  top: Record<string, unknown> = {},
) {
  const d = detail({ request: { kind: 'trigger', target_id: TRIG, ...requestOver } });
  mockApi({
    'GET /api/v1/orgs/acme': orgAs(role),
    'GET /api/v1/orgs/acme/members': MEMBERS,
    [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: { ...d, picture: triggerPicture(trigger, top) },
    ...extra,
  });
  return renderRoutes(
    [{ path: '/runs/requests/$id', component: RunRequestDetailPage }],
    `/o/acme/runs/requests/${RQ}`,
    new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    lang,
  );
}

describe('RunRequestDetailPage trigger requests (spec 2026-11-25 C)', () => {
  it('shows a change next to the trigger as it is, its URL, and no run sections', async () => {
    renderTrigger({
      action: 'update',
      current,
      new: { ...current, name: 'release hook', allow_cidrs: [], revision: undefined },
    });
    expect(await screen.findByRole('heading', { name: 'Change a webhook trigger' })).toBeInTheDocument();
    const section = screen.getByRole('region', { name: 'Webhook trigger' });
    expect(within(section).getByText(URL)).toBeInTheDocument();
    const table = within(section).getByRole('table');
    const name = within(table).getByRole('row', { name: /Name/ });
    expect(within(name).getByText('release hook')).toBeInTheDocument();
    expect(within(name).getByText('changes')).toBeInTheDocument();
    const sources = within(table).getByRole('row', { name: /Allowed sources/ });
    expect(within(sources).getByText('192.0.2.0/24')).toBeInTheDocument();
    expect(within(sources).getByText(/any source/)).toBeInTheDocument();
    const mapping = within(table).getByRole('row', { name: /Payload mapping/ });
    expect(within(mapping).queryByText('changes')).not.toBeInTheDocument();
    expect(within(section).getByText(/untrusted text/)).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Targets' })).not.toBeInTheDocument();
  });

  it('names the repository a sync trigger syncs, and asks for repos.write', async () => {
    renderTrigger(
      {
        action: 'create',
        new: {
          ...current,
          kind: 'repo_sync',
          template_id: undefined,
          template_name: undefined,
          repo_id: 'r1',
          repo_name: 'infra',
          repo_url: 'https://git.example.com/infra.git',
          repo_branch: 'main',
          mapping: {},
          revision: undefined,
        },
      },
      'viewer',
      { template_id: '' },
      {},
      undefined,
      { template_id: '', template_name: '', template_revision: 0 },
    );
    expect(await screen.findByRole('heading', { name: 'Create a webhook trigger' })).toBeInTheDocument();
    expect(screen.getByText(/syncs infra \(https:\/\/git\.example\.com\/infra\.git, main\)/)).toBeInTheDocument();
    expect(screen.queryByText(/Template revision/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
    expect(screen.getByText(/Operators, admins and owners can approve or reject webhook trigger requests/)).toBeInTheDocument();
  });

  it('shows the secret once after the approval, with copy, and forgets it', async () => {
    const user = userEvent.setup();
    let approved: unknown;
    renderTrigger(
      { action: 'create', new: { ...current, revision: undefined } },
      'operator',
      {},
      {
        [`POST /api/v1/orgs/acme/run-requests/${RQ}/approve`]: (req: MockRequest) => {
          approved = req.body;
          return {
            request: request({ kind: 'trigger', status: 'approved', target_id: TRIG }),
            trigger_secret: SECRET,
            trigger_url: URL,
          };
        },
      },
    );
    expect(await screen.findByRole('heading', { name: 'Create a webhook trigger' })).toBeInTheDocument();
    expect(screen.queryByText(SECRET)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => expect(approved).toEqual({ fingerprint: 'fp-1' }));
    const dialog = await screen.findByRole('dialog', { name: "The trigger's secret" });
    expect(within(dialog).getByText(SECRET)).toBeInTheDocument();
    expect(within(dialog).getByText(URL)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Copy secret' })).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'I stored the secret' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(screen.queryByText(SECRET)).not.toBeInTheDocument();
  });

  it('says a rotation keeps a disabled trigger disabled', async () => {
    renderTrigger({ action: 'rotate', current: { ...current, enabled: false } });
    expect(await screen.findByRole('heading', { name: "Rotate a webhook trigger's secret" })).toBeInTheDocument();
    expect(screen.getByText(/stays enabled or disabled as it is/)).toBeInTheDocument();
    const state = within(screen.getByRole('table')).getByRole('row', { name: /State/ });
    expect(within(state).getAllByText('disabled')).toHaveLength(2);
    expect(within(state).queryByText('changes')).not.toBeInTheDocument();
  });

  it('holds the approval of a trigger that is gone', async () => {
    renderTrigger({ action: 'rotate', gone: true });
    expect(await screen.findByText(/no longer exists/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Reject' })).toBeEnabled();
  });

  it('says what an approval did', async () => {
    renderTrigger({ action: 'create', new: current }, 'operator', { status: 'approved', decided_at: '2026-11-16T09:05:00Z' });
    expect(await screen.findByText(/Approved\. The webhook trigger was saved/)).toBeInTheDocument();
  });

  it('speaks German', async () => {
    renderTrigger({ action: 'update', current, new: { ...current, name: 'Release' } }, 'operator', {}, {}, 'de');
    expect(await screen.findByRole('heading', { name: 'Webhook-Trigger ändern' })).toBeInTheDocument();
    expect(within(screen.getByRole('table')).getByText('ändert sich')).toBeInTheDocument();
  });
});
