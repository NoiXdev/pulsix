import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { WorkersPage } from './WorkersPage.tsx';
import { expiryLabel, seenLabel } from './expiry.ts';
import { en } from '../../i18n/en/index.ts';
import { clippedElements } from '../../test/clipped.ts';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { acceptConfirm } from '../../test/confirmDialog.ts';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const inDays = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString();
const worker = (over: Record<string, unknown> = {}) => ({
  id: 'w1', name: 'hel-1', zone_id: 'z1', zone_slug: 'hel', status: 'online', last_seen: new Date().toISOString(),
  version: 'v0.2c', cert_expires_at: inDays(41), created_at: '', ...over,
});

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const listRoute = createRoute({ getParentRoute: () => orgRoute, path: '/workers', component: WorkersPage });
  const runRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: () => <div>RUN VIEW</div> });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([listRoute, runRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/workers'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ConfirmProvider>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

describe('WorkersPage', () => {
  it('badges a worker that needs an update or runs ahead of the manager, and nothing else', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': {
        workers: [
          worker({ id: 'w1', name: 'w-old', version: 'v1.1.0', version_status: 'update_required' }),
          worker({ id: 'w2', name: 'w-newer', version: 'v1.9.0', version_status: 'newer' }),
          worker({ id: 'w3', name: 'w-current', version: 'v1.4.0', version_status: 'current' }),
          worker({ id: 'w4', name: 'w-previous', version: 'v1.3.0', version_status: 'previous_minor' }),
          worker({ id: 'w5', name: 'w-dev', version: 'dev', version_status: 'unknown' }),
        ],
      },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
    });
    renderPage();
    expect(await screen.findByText('w-old')).toBeInTheDocument();
    expect(screen.getAllByText('Update required')).toHaveLength(1);
    expect(screen.getAllByText('Newer than manager')).toHaveLength(1);
    // The version itself stays visible next to the badge.
    expect(screen.getByText('v1.1.0')).toBeInTheDocument();
  });

  it('does not badge a revoked worker: it never runs again, so there is nothing to update', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': {
        workers: [
          worker({ id: 'w1', name: 'w-revoked-old', status: 'revoked', cert_expires_at: null, version: 'v1.1.0', version_status: 'update_required' }),
          worker({ id: 'w2', name: 'w-revoked-newer', status: 'revoked', cert_expires_at: null, version: 'v1.9.0', version_status: 'newer' }),
          worker({ id: 'w3', name: 'w-active-old', version: 'v1.2.0', version_status: 'update_required' }),
        ],
      },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
    });
    renderPage();
    expect(await screen.findByText('w-revoked-old')).toBeInTheDocument();
    // Only the active worker is badged; the revoked ones keep their version.
    expect(screen.getAllByText('Update required')).toHaveLength(1);
    expect(screen.queryByText('Newer than manager')).not.toBeInTheDocument();
    expect(screen.getByText('v1.1.0')).toBeInTheDocument();
    expect(screen.getByText('v1.9.0')).toBeInTheDocument();
  });

  it('lists workers with status, zone, expiry highlight and the current job', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': {
        workers: [
          worker({ current_job_id: '0197c0a2-0000-7000-8000-000000000001' }),
          worker({ id: 'w2', name: 'fra-1', zone_slug: 'fra', status: 'offline', cert_expires_at: inDays(5) }),
          worker({ id: 'w3', name: 'old', status: 'revoked', cert_expires_at: null }),
        ],
      },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [{ id: 'e1', name: 'hel-2', zone_id: 'z1', expires_at: inDays(0.02), created_at: '' }] },
    });
    renderPage();
    expect(await screen.findByText('hel-1')).toBeInTheDocument();
    expect(screen.getByText('online')).toBeInTheDocument();
    expect(screen.getByText('offline')).toBeInTheDocument();
    expect(screen.getByText('revoked')).toBeInTheDocument();
    expect(document.querySelector('[data-expiring]')?.textContent).toMatch(/in [45] d/);
    expect(screen.getByRole('link', { name: '0197c0a2' })).toBeInTheDocument();
    expect(screen.getByText('hel-2')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Actions for hel-1' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Actions for old' })).toBeInTheDocument();
  });

  it('adds a worker and shows the token and snippets once', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('owner'),
      'GET /api/v1/orgs/acme/workers': { workers: [] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
      'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', org_id: 'o', slug: 'hel', name: 'Helsinki' }] },
      'POST /api/v1/orgs/acme/worker-enrollments': (req: MockRequest) => {
        posted = req.body;
        return {
          enrollment: { id: 'e1', name: 'hel-1', zone_id: 'z1', expires_at: inDays(0.04), created_at: '' },
          token: 'pxw1.aaa.bbb.ccc',
          docker_snippet: 'docker run … PULSIX_ENROLL_TOKEN=pxw1.aaa.bbb.ccc',
          compose_snippet: 'services: … PULSIX_ENROLL_TOKEN: ${PULSIX_ENROLL_TOKEN:-}',
          compose_up_command: 'PULSIX_ENROLL_TOKEN=pxw1.aaa.bbb.ccc docker compose up -d',
        };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add worker' }));
    const dialog = screen.getByRole('dialog', { name: 'Add worker' });
    await user.type(within(dialog).getByLabelText('Name'), 'hel-1');
    await user.click(within(dialog).getByRole('button', { name: 'Create token' }));
    expect(await screen.findByLabelText('Enrollment token')).toHaveTextContent('pxw1.aaa.bbb.ccc');
    expect(posted).toEqual({ zone_id: 'z1', name: 'hel-1' });
    expect(screen.getByLabelText('Worker snippet')).toHaveTextContent('docker run');
    await user.click(screen.getByRole('tab', { name: 'Compose service' }));
    expect(screen.getByLabelText('Worker snippet')).toHaveTextContent('services:');
    // Backlog 7.59: the file holds no token; the start command below it does,
    // with its own copy button and a hint saying why.
    expect(screen.getByLabelText('Worker snippet')).not.toHaveTextContent('pxw1.aaa.bbb.ccc');
    expect(screen.getByLabelText('Start command')).toHaveTextContent('PULSIX_ENROLL_TOKEN=pxw1.aaa.bbb.ccc docker compose up -d');
    expect(screen.getByText(en.workers.dialog.composeUpHint)).toBeInTheDocument();
    // Labelled like the snippet's own copy button, not icon-only.
    expect(screen.getByRole('button', { name: 'Copy start command' })).toHaveTextContent(en.workers.dialog.copy);
    await user.click(screen.getByRole('button', { name: 'Copy start command' }));
    expect(await navigator.clipboard.readText()).toBe('PULSIX_ENROLL_TOKEN=pxw1.aaa.bbb.ccc docker compose up -d');
    await user.click(screen.getByRole('tab', { name: 'Docker' }));
    expect(screen.queryByLabelText('Start command')).not.toBeInTheDocument();
  });

  it('refuses an invalid name before calling the server', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
      'GET /api/v1/orgs/acme/zones': { zones: [{ id: 'z1', org_id: 'o', slug: 'hel', name: 'Helsinki' }] },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add worker' }));
    await user.type(screen.getByLabelText('Name'), 'Bad Name');
    await user.click(screen.getByRole('button', { name: 'Create token' }));
    expect(await screen.findByText(/lowercase letters/)).toBeInTheDocument();
  });

  // v0.4g Task 9: the Zone picker's create row. workers.write is admin,
  // stricter than hosts.write (operator, zone creation's own permission) —
  // but an admin already satisfies the lower bar, so condition 1 holds and
  // nothing needs gating here, unlike credentials (admin-only, reached from
  // operator-level forms in Task 7).
  it('creates a zone from the picker and uses it for the enrollment', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    let zonePosted: unknown;
    let created = false;
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
      // A function, not a fixed list: the picker's options must include
      // the new zone after creation invalidates this query.
      'GET /api/v1/orgs/acme/zones': () => ({ zones: created ? [{ id: 'z9', org_id: 'o', slug: 'frankfurt', name: 'Frankfurt' }] : [] }),
      'POST /api/v1/orgs/acme/zones': (req: MockRequest) => {
        zonePosted = req.body;
        created = true;
        return { zone: { id: 'z9', slug: 'frankfurt', name: 'Frankfurt' } };
      },
      'POST /api/v1/orgs/acme/worker-enrollments': (req: MockRequest) => {
        posted = req.body;
        return {
          enrollment: { id: 'e1', name: 'hel-1', zone_id: 'z9', expires_at: inDays(0.04), created_at: '' },
          token: 'pxw1.aaa.bbb.ccc',
          docker_snippet: 'docker run',
          compose_snippet: 'services:',
          compose_up_command: 'PULSIX_ENROLL_TOKEN=pxw1.aaa.bbb.ccc docker compose up -d',
        };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Add worker' }));
    const dialog = screen.getByRole('dialog', { name: 'Add worker' });
    await user.type(within(dialog).getByLabelText('Name'), 'hel-1');
    await user.click(within(dialog).getByLabelText('Zone'));
    await user.keyboard('frankfurt');
    await user.click(within(dialog).getByRole('option', { name: /New zone/ }));

    const zoneDialog = await screen.findByRole('dialog', { name: 'Add zone' });
    expect(within(zoneDialog).getByLabelText('Name')).toHaveValue('frankfurt');
    await user.type(within(zoneDialog).getByLabelText('Slug'), 'frankfurt');
    await user.click(within(zoneDialog).getByRole('button', { name: 'Create zone' }));

    expect(zonePosted).toMatchObject({ name: 'frankfurt' });
    // Not findByLabelText: the field is already in the DOM before the
    // zones list refetch (triggered by the mutation's own invalidation)
    // completes, so the assertion itself has to retry, not just the lookup.
    await waitFor(() => expect(within(dialog).getByLabelText('Zone')).toHaveValue('Frankfurt (frankfurt)'));

    await user.click(within(dialog).getByRole('button', { name: 'Create token' }));
    expect(posted).toMatchObject({ zone_id: 'z9', name: 'hel-1' });
  });

  it('revokes after confirmation and withdraws a pending token', async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [worker()] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [{ id: 'e1', name: 'hel-2', zone_id: 'z1', expires_at: inDays(0.02), created_at: '' }] },
      'POST /api/v1/orgs/acme/workers/w1/revoke': () => {
        calls.push('revoke');
        return { worker: worker({ status: 'revoked' }), streams_closed: 1, jobs_requeued: 1, jobs_ended: 0 };
      },
      'DELETE /api/v1/orgs/acme/worker-enrollments/e1': () => {
        calls.push('withdraw');
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for hel-1' }));
    await user.click(screen.getByRole('menuitem', { name: 'Revoke…' }));
    await acceptConfirm(user);
    expect(await screen.findByText('Worker hel-1 revoked')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Actions for enrollment hel-2' }));
    await user.click(screen.getByRole('menuitem', { name: 'Withdraw' }));
    await waitFor(() => expect(calls).toEqual(['revoke', 'withdraw']));
  });

  it('deletes a revoked worker after confirmation', async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [worker({ id: 'w3', name: 'old', status: 'revoked', cert_expires_at: null })] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
      'DELETE /api/v1/orgs/acme/workers/w3': () => {
        calls.push('delete');
        return {};
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for old' }));
    expect(screen.queryByRole('menuitem', { name: 'Revoke…' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await acceptConfirm(user);
    await waitFor(() => expect(calls).toEqual(['delete']));
  });

  it('shows an error toast when revoke fails', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [worker()] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
      'POST /api/v1/orgs/acme/workers/w1/revoke': () => new Response(JSON.stringify({ detail: 'worker not found' }), { status: 404 }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for hel-1' }));
    await user.click(screen.getByRole('menuitem', { name: 'Revoke…' }));
    await acceptConfirm(user);
    expect(await screen.findByText('worker not found')).toBeInTheDocument();
  });

  it('shows an error toast when delete fails', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [worker({ id: 'w3', name: 'old', status: 'revoked', cert_expires_at: null })] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
      'DELETE /api/v1/orgs/acme/workers/w3': () => new Response(JSON.stringify({ detail: 'delete refused' }), { status: 409 }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for old' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete…' }));
    await acceptConfirm(user);
    expect(await screen.findByText('delete refused')).toBeInTheDocument();
  });

  it('shows an error toast when withdrawing a pending token fails', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [{ id: 'e1', name: 'hel-2', zone_id: 'z1', expires_at: inDays(0.02), created_at: '' }] },
      'DELETE /api/v1/orgs/acme/worker-enrollments/e1': () => new Response(JSON.stringify({ detail: 'token already used' }), { status: 409 }),
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Actions for enrollment hel-2' }));
    await user.click(screen.getByRole('menuitem', { name: 'Withdraw' }));
    expect(await screen.findByText('token already used')).toBeInTheDocument();
  });

  it('tells non-admins who manages workers', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org('operator') });
    renderPage();
    expect(await screen.findByText(/owners and admins/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Actions for / })).not.toBeInTheDocument();
  });

  // workers.read and workers.write are both admin, so there is no read-only
  // role: every row a user can see has its menu.
  it('gives an admin a menu on every row', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [worker(), worker({ id: 'w2', name: 'fra-1' }), worker({ id: 'w3', name: 'old', status: 'revoked', cert_expires_at: null })] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [{ id: 'e1', name: 'hel-2', zone_id: 'z1', expires_at: inDays(0.02), created_at: '' }] },
    });
    renderPage();
    await screen.findByText('hel-1');
    expect(screen.getAllByRole('button', { name: /^Actions for / })).toHaveLength(4);
    for (const name of ['Actions for hel-1', 'Actions for fra-1', 'Actions for old', 'Actions for enrollment hel-2']) {
      expect(screen.getByRole('button', { name })).toBeInTheDocument();
    }
  });

  it('keeps a pending enrolment menu apart from a worker of the same name', async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [worker()] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [{ id: 'e1', name: 'hel-1', zone_id: 'z1', expires_at: inDays(0.02), created_at: '' }] },
      'DELETE /api/v1/orgs/acme/worker-enrollments/e1': () => {
        calls.push('withdraw');
        return {};
      },
    });
    renderPage();
    await screen.findByRole('button', { name: 'Actions for hel-1' });
    // One button each, so neither query throws on a duplicate.
    await user.click(screen.getByRole('button', { name: 'Actions for enrollment hel-1' }));
    await user.click(screen.getByRole('menuitem', { name: 'Withdraw' }));
    await waitFor(() => expect(calls).toEqual(['withdraw']));
  });

  it('rows are not links and keep the current-job link', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [worker({ current_job_id: 'abcd1234-0000-7000-8000-000000000001' })] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [] },
    });
    const { container } = renderPage();
    const link = await screen.findByRole('link', { name: 'abcd1234' });
    expect(link).toHaveAttribute('href', '/o/acme/runs/abcd1234-0000-7000-8000-000000000001');
    expect(container.querySelector('[data-list-row] a[aria-labelledby]')).toBeNull();
    expect(container.querySelectorAll('[data-list-row]').length).toBeGreaterThan(0);
  });

  it('no clipped cards', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': org('admin'),
      'GET /api/v1/orgs/acme/workers': { workers: [worker()] },
      'GET /api/v1/orgs/acme/worker-enrollments': { enrollments: [{ id: 'e1', name: 'hel-2', zone_id: 'z1', expires_at: inDays(0.02), created_at: '' }] },
    });
    const { container } = renderPage();
    await screen.findByText('hel-1');
    expect(clippedElements(container)).toEqual([]);
  });
});

describe('expiry labels', () => {
  const now = new Date('2026-09-18T10:00:00Z');
  it('renders expiry and last-seen ages', () => {
    expect(expiryLabel('2026-10-29T10:00:00Z', now, en)).toBe('in 41 d');
    expect(expiryLabel('2026-09-17T10:00:00Z', now, en)).toBe('expired');
    expect(expiryLabel('2026-09-18T20:00:00Z', now, en)).toBe('today');
    expect(expiryLabel(null, now, en)).toBe('—');
    expect(seenLabel('2026-09-18T09:55:00Z', now, en)).toBe('5 min ago');
    expect(seenLabel('2026-09-18T09:59:50Z', now, en)).toBe('just now');
    expect(seenLabel('2026-09-18T07:00:00Z', now, en)).toBe('3 h ago');
    expect(seenLabel('2026-09-15T10:00:00Z', now, en)).toBe('3 d ago');
    expect(seenLabel(undefined, now, en)).toBe('never');
  });
});
