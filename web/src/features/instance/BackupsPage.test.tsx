import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createRootRoute, createRoute, createRouter, createMemoryHistory, RouterProvider } from '@tanstack/react-router';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ADMIN } from '../../test/renderRoutes.tsx';
import { BackupsPage } from './BackupsPage.tsx';
import { backupsRefetchInterval } from '../../api/hooks/useBackups.ts';

const FOLDER_SETTINGS = {
  destination: 'folder',
  s3: { endpoint: '', region: '', bucket: '', prefix: '', access_key: '', secret_key_set: false, path_style: false },
  enabled: true,
  time_of_day: '03:00',
  time_zone: 'Europe/Berlin',
  retention: { daily: 7, weekly: 4, monthly: 6 },
  include_metrics: true,
};

const S3_SETTINGS = {
  ...FOLDER_SETTINGS,
  destination: 's3',
  s3: {
    endpoint: 'https://s3.example.com',
    region: 'eu-central-1',
    bucket: 'firma-backups',
    prefix: 'pulsix/',
    access_key: 'AKIA123',
    secret_key_set: true,
    path_style: true,
  },
};

const OK_ITEM = {
  name: 'pulsix-20261102T030000Z-scheduled.pxbak',
  created_at: '2026-11-02T03:00:00Z',
  kind: 'scheduled',
  actor_id: null,
  actor_name: '',
  pulsix_version: '0.5.2',
  size_bytes: 412 * 1024 * 1024,
  state: 'succeeded',
  error: '',
  verified: true,
};

function list(items: unknown[], extra: Record<string, unknown> = {}) {
  return {
    items,
    next_run_at: '2026-11-03T03:00:00Z',
    destination_label: 'Folder /var/lib/pulsix/backups',
    storage_error: null,
    ...extra,
  };
}

function renderPage() {
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const backupsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings/backups', component: BackupsPage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([backupsRoute])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/settings/backups'] }),
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

const base = (extra: Record<string, unknown> = {}) => ({
  'GET /api/v1/auth/me': ADMIN,
  'GET /api/v1/orgs/acme': { org: { id: 'o1', slug: 'acme', name: 'Acme', role: 'owner' } },
  'GET /api/v1/admin/backups/settings': FOLDER_SETTINGS,
  'GET /api/v1/admin/backups': list([OK_ITEM]),
  ...extra,
});

afterEach(() => vi.unstubAllGlobals());

describe('BackupsPage', () => {
  it('renders the stored settings, the status strip and the list', async () => {
    mockApi(base({ 'GET /api/v1/admin/backups/settings': S3_SETTINGS }));
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Backups', level: 1 })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText('Bucket')).toHaveValue('firma-backups'));
    expect(screen.getByLabelText('Endpoint')).toHaveValue('https://s3.example.com');
    expect(screen.getByLabelText('Path prefix')).toHaveValue('pulsix/');
    expect(screen.getByLabelText('Access key')).toHaveValue('AKIA123');
    // The secret is never shown back, only that one is stored.
    expect(screen.getByLabelText('Secret key')).toHaveValue('');
    expect(screen.getByText(/A secret key is stored/)).toBeInTheDocument();
    expect(screen.getByLabelText('Time zone')).toHaveValue('Europe/Berlin');
    expect(screen.getByLabelText('Daily backups kept')).toHaveValue(7);

    const strip = screen.getByRole('region', { name: 'Backup status' });
    expect(within(strip).getByText('Folder /var/lib/pulsix/backups')).toBeInTheDocument();
    expect(within(strip).getByText(/412 MB/)).toBeInTheDocument();
    expect(screen.getByText(/Every backup is encrypted/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute(
      'href',
      '/api/v1/admin/backups/pulsix-20261102T030000Z-scheduled.pxbak/download',
    );
  });

  it('defaults the time zone to the browser zone while nothing is saved', async () => {
    mockApi(
      base({
        'GET /api/v1/admin/backups/settings': { ...FOLDER_SETTINGS, enabled: false, time_zone: 'UTC' },
      }),
    );
    renderPage();
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    await waitFor(() => expect(screen.getByLabelText('Time zone')).toHaveValue(zone));
  });

  it('saves without the secret key when its field is untouched, and with it when typed', async () => {
    const user = userEvent.setup();
    const puts: Array<{ s3?: Record<string, unknown> }> = [];
    mockApi(
      base({
        'GET /api/v1/admin/backups/settings': S3_SETTINGS,
        'PUT /api/v1/admin/backups/settings': (req: MockRequest) => {
          puts.push(req.body as { s3?: Record<string, unknown> });
          return new Response(null, { status: 204 });
        },
      }),
    );
    renderPage();
    await waitFor(() => expect(screen.getByLabelText('Bucket')).toHaveValue('firma-backups'));

    await user.clear(screen.getByLabelText('Bucket'));
    await user.type(screen.getByLabelText('Bucket'), 'other-bucket');
    await user.click(screen.getByRole('button', { name: 'Save backup settings' }));
    expect(await screen.findByText('Backup settings saved')).toBeInTheDocument();
    expect(puts).toHaveLength(1);
    expect(puts[0]).toMatchObject({
      destination: 's3',
      enabled: true,
      time_of_day: '03:00',
      time_zone: 'Europe/Berlin',
      retention: { daily: 7, weekly: 4, monthly: 6 },
      include_metrics: true,
    });
    expect(puts[0].s3).toMatchObject({ bucket: 'other-bucket', endpoint: 'https://s3.example.com', path_style: true });
    expect(puts[0].s3).not.toHaveProperty('secret_key');

    await user.type(screen.getByLabelText('Secret key'), 'hunter2');
    await user.click(screen.getByRole('button', { name: 'Save backup settings' }));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1].s3).toMatchObject({ secret_key: 'hunter2' });
  });

  it('removes the stored secret key only when asked to', async () => {
    const user = userEvent.setup();
    const puts: Array<{ s3?: Record<string, unknown> }> = [];
    mockApi(
      base({
        'GET /api/v1/admin/backups/settings': S3_SETTINGS,
        'PUT /api/v1/admin/backups/settings': (req: MockRequest) => {
          puts.push(req.body as { s3?: Record<string, unknown> });
          return new Response(null, { status: 204 });
        },
      }),
    );
    renderPage();
    await waitFor(() => expect(screen.getByLabelText('Bucket')).toHaveValue('firma-backups'));
    await user.click(screen.getByLabelText('Remove the stored secret key'));
    await user.click(screen.getByRole('button', { name: 'Save backup settings' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0].s3).toMatchObject({ secret_key: '' });
  });

  it('refuses a malformed time without calling the API', async () => {
    const user = userEvent.setup();
    let called = false;
    mockApi(
      base({
        'PUT /api/v1/admin/backups/settings': () => {
          called = true;
          return new Response(null, { status: 204 });
        },
      }),
    );
    renderPage();
    await waitFor(() => expect(screen.getByLabelText('Time zone')).toHaveValue('Europe/Berlin'));
    await user.clear(screen.getByLabelText('Time of day'));
    await user.type(screen.getByLabelText('Time of day'), '3pm');
    await user.click(screen.getByRole('button', { name: 'Save backup settings' }));
    expect(await screen.findByText('Enter the time as HH:MM')).toBeInTheDocument();
    expect(called).toBe(false);
  });

  it('shows the server detail when Test connection fails, and success when it works', async () => {
    const user = userEvent.setup();
    const bodies: unknown[] = [];
    let ok = false;
    mockApi(
      base({
        'GET /api/v1/admin/backups/settings': S3_SETTINGS,
        'POST /api/v1/admin/backups/settings/test': (req: MockRequest) => {
          bodies.push(req.body);
          if (ok) return new Response(null, { status: 204 });
          return new Response(JSON.stringify({ title: 'Unprocessable Entity', detail: 'S3: access denied' }), {
            status: 422,
            headers: { 'Content-Type': 'application/json' },
          });
        },
      }),
    );
    renderPage();
    await waitFor(() => expect(screen.getByLabelText('Bucket')).toHaveValue('firma-backups'));

    await user.click(screen.getByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText('S3: access denied')).toBeInTheDocument();
    // The stored secret is tested too: an untouched field sends none.
    expect(bodies[0]).toMatchObject({ destination: 's3', s3: { bucket: 'firma-backups' } });
    expect((bodies[0] as { s3: object }).s3).not.toHaveProperty('secret_key');

    ok = true;
    await user.click(screen.getByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText('The connection works')).toBeInTheDocument();
    expect(screen.queryByText('S3: access denied')).not.toBeInTheDocument();
  });

  it('starts a backup and shows the new running entry', async () => {
    const user = userEvent.setup();
    let started = false;
    mockApi(
      base({
        'GET /api/v1/admin/backups': () =>
          list(
            started
              ? [
                  {
                    name: 'pulsix-20261103T100000Z-manual.pxbak',
                    created_at: '2026-11-03T10:00:00Z',
                    kind: 'manual',
                    actor_id: 'u1',
                    actor_name: 'Ada',
                    pulsix_version: '',
                    size_bytes: 0,
                    state: 'running',
                    error: '',
                  },
                  OK_ITEM,
                ]
              : [OK_ITEM],
          ),
        'POST /api/v1/admin/backups': () => {
          started = true;
          return new Response(JSON.stringify({ attempt_id: 'a1' }), {
            status: 202,
            headers: { 'Content-Type': 'application/json' },
          });
        },
      }),
    );
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Back up now' }));
    expect(await screen.findByText('Running')).toBeInTheDocument();
    expect(screen.getByText('manual (Ada)')).toBeInTheDocument();
    // One backup at a time: the button waits for the running one.
    await waitFor(() => expect(screen.getByRole('button', { name: 'Back up now' })).toBeDisabled());
  });

  it('says so when another backup is already running (409)', async () => {
    const user = userEvent.setup();
    mockApi(
      base({
        'POST /api/v1/admin/backups': () =>
          new Response(JSON.stringify({ title: 'Conflict', detail: 'a backup is already running' }), {
            status: 409,
            headers: { 'Content-Type': 'application/json' },
          }),
      }),
    );
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Back up now' }));
    expect(await screen.findByText('a backup is already running')).toBeInTheDocument();
  });

  it('renders all five states, the error of a failed one, and the storage error above the entries', async () => {
    mockApi(
      base({
        'GET /api/v1/admin/backups': list(
          [
            { ...OK_ITEM, name: 'ok.pxbak' },
            {
              ...OK_ITEM,
              name: 'failed',
              created_at: '2026-11-01T03:00:00Z',
              size_bytes: 0,
              pulsix_version: '',
              state: 'failed',
              error: 'S3: access denied',
            },
            {
              ...OK_ITEM,
              name: 'lost',
              created_at: '2026-10-31T03:00:00Z',
              size_bytes: 0,
              pulsix_version: '',
              state: 'abandoned',
              error: 'the manager restarted',
            },
            {
              ...OK_ITEM,
              name: 'pulsix-20261030T030000Z-scheduled.pxbak',
              created_at: '2026-10-30T03:00:00Z',
              pulsix_version: '',
              state: 'damaged',
              error: 'header unreadable',
            },
            { ...OK_ITEM, name: 'run', created_at: '2026-10-29T03:00:00Z', size_bytes: 0, state: 'running' },
          ],
          { storage_error: 'dial tcp: connection refused' },
        ),
      }),
    );
    renderPage();

    expect(await screen.findByText('S3: access denied')).toBeInTheDocument();
    expect(screen.getByText('the manager restarted')).toBeInTheDocument();
    expect(screen.getByText('header unreadable')).toBeInTheDocument();
    for (const label of ['Succeeded', 'Failed', 'Interrupted', 'Damaged', 'Running']) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0);
    }
    expect(screen.getByRole('alert')).toHaveTextContent('dial tcp: connection refused');

    // Only a readable stored file can be restored; a damaged one can still be downloaded or deleted.
    expect(screen.getAllByRole('button', { name: 'Restore…' })).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'Delete' })).toHaveLength(2);
    expect(screen.getAllByRole('link', { name: 'Download' })).toHaveLength(2);
  });

  it('marks a backup whose header cannot be authenticated as unverified, and still offers its actions', async () => {
    mockApi(
      base({
        'GET /api/v1/admin/backups': list([
          { ...OK_ITEM, name: 'pulsix-20261103T030000Z-scheduled.pxbak', created_at: '2026-11-03T03:00:00Z' },
          { ...OK_ITEM, verified: false },
        ]),
      }),
    );
    renderPage();

    const label = await screen.findByText('Unverified');
    expect(screen.getAllByText('Unverified')).toHaveLength(1);
    expect(label.closest('[title]')).toHaveAttribute('title', expect.stringMatching(/cannot be authenticated/));
    expect(screen.getAllByRole('button', { name: 'Restore…' })).toHaveLength(2);
    expect(screen.getAllByRole('link', { name: 'Download' })).toHaveLength(2);
  });

  it('deletes a backup after confirmation', async () => {
    const user = userEvent.setup();
    const deleted: string[] = [];
    const confirm = vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true);
    vi.stubGlobal('confirm', confirm);
    mockApi(
      base({
        [`DELETE /api/v1/admin/backups/${OK_ITEM.name}`]: (req: MockRequest) => {
          deleted.push(req.path);
          return new Response(null, { status: 204 });
        },
      }),
    );
    renderPage();
    const del = await screen.findByRole('button', { name: 'Delete' });
    await user.click(del);
    expect(deleted).toEqual([]);
    await user.click(del);
    await waitFor(() => expect(deleted).toEqual([`/api/v1/admin/backups/${OK_ITEM.name}`]));
    expect(confirm).toHaveBeenCalledTimes(2);
  });

  it('shows the restore commands of an S3 destination with its saved endpoint, region, path style, bucket and prefix', async () => {
    const user = userEvent.setup();
    mockApi(base({ 'GET /api/v1/admin/backups/settings': S3_SETTINGS }));
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Restore…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Restore a backup' });
    const command = within(dialog).getByText(/cli restore --from s3:\/\/firma-backups\/pulsix --replace pulsix-2026/);
    expect(command.textContent).toContain(
      '-e PULSIX_BACKUP_S3_ENDPOINT=https://s3.example.com -e PULSIX_BACKUP_S3_REGION=eu-central-1 -e PULSIX_BACKUP_S3_PATH_STYLE=true ' +
        '-e PULSIX_BACKUP_S3_ACCESS_KEY -e PULSIX_BACKUP_S3_SECRET_KEY cli restore',
    );
    expect(command.textContent).not.toContain('AKIA123');
  });
});

describe('backupsRefetchInterval', () => {
  it('polls every 5 s while an entry is running, and not otherwise', () => {
    expect(backupsRefetchInterval(undefined)).toBe(false);
    expect(backupsRefetchInterval(list([OK_ITEM]) as never)).toBe(false);
    expect(backupsRefetchInterval(list([OK_ITEM, { ...OK_ITEM, state: 'running' }]) as never)).toBe(5000);
  });
});
