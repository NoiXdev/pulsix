import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { en } from '../../i18n/en/index.ts';
import { formatter } from '../../i18n/format.ts';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { MaintenancePage } from './MaintenancePage.tsx';
import { fromLocalInput, targetLabel, toLocalInput, whenLabel } from './time.ts';
import type { MaintenanceWindow } from '../../api/types.ts';

const org = (role: string) => ({ org: { id: 'o', name: 'Acme', slug: 'acme', role, created_at: '' } });
const nightly: MaintenanceWindow = {
  id: 'w1', name: 'nightly', target_kind: 'org', reason: 'maintenance', cron: '0 2 * * 6', description: 'Sat at 02:00', duration: '2h',
  timezone: 'Europe/Berlin', active_now: true, revision: 1, created_at: '', updated_at: '',
};
const dbUpgrade: MaintenanceWindow = {
  id: 'w2', name: 'db upgrade', target_kind: 'host', reason: 'maintenance', target_id: 'h1', target_name: 'db-1', starts_at: '2026-09-22T10:00:00Z',
  ends_at: '2026-09-22T12:00:00Z', note: 'kernel', active_now: false, revision: 3, created_at: '', updated_at: '',
};
const noHosts = { hosts: [], next_cursor: '' };

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org' });
  const page = createRoute({ getParentRoute: () => orgRoute, path: '/maintenance', component: MaintenancePage });
  const router = createRouter({
    routeTree: rootRoute.addChildren([orgRoute.addChildren([page])]),
    history: createMemoryHistory({ initialEntries: ['/o/acme/maintenance'] }),
  });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('MaintenancePage', () => {
  it('lists windows with target, timing and active state', async () => {
    mockApi({ 'GET /api/v1/orgs/acme': org('viewer'), 'GET /api/v1/orgs/acme/maintenance-windows': { windows: [nightly, dbUpgrade] } });
    renderPage();
    const row = (await screen.findByText('nightly')).closest('[data-row]')!.parentElement as HTMLElement;
    expect(within(row).getByText('Whole organization')).toBeInTheDocument();
    expect(within(row).getByText('Sat at 02:00 · for 2h · Europe/Berlin')).toBeInTheDocument();
    expect(within(row).getByText('active now').style.color).toBe('var(--px-maintenance)');
    expect(screen.getByText('Host db-1')).toBeInTheDocument();
    expect(screen.getByText(/1 active now · maintenance lets schedules skip/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New window' })).not.toBeInTheDocument();
  });

  it('creates a recurring window for an inventory group', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/maintenance-windows': { windows: [] },
      'GET /api/v1/orgs/acme/hosts': noHosts,
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [{ id: 'g1', name: 'postgres', description: '', members: 2, complete: true, group_fields: 0, host_fields: 0 }] },
      'POST /api/v1/orgs/acme/maintenance-windows': (req: MockRequest) => {
        posted = req.body;
        return { window: nightly };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'New window' }));
    const dialog = screen.getByRole('dialog', { name: 'New window' });
    await user.type(within(dialog).getByLabelText('Name'), 'db patching');
    await pickOption(within(dialog), 'Applies to', { value: 'inventory_group' });
    await within(dialog).findByLabelText('Group');
    await pickOption(within(dialog), 'Group', { value: 'g1' });
    await user.click(within(dialog).getByRole('radio', { name: 'Recurring' }));
    await user.clear(within(dialog).getByLabelText('Timezone'));
    await user.type(within(dialog).getByLabelText('Timezone'), 'UTC');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(posted).toEqual({ name: 'db patching', target_kind: 'inventory_group', target_id: 'g1', note: '', reason: 'maintenance', cron: '0 2 * * 6', duration: '2h', timezone: 'UTC' }),
    );
  });

  it('edits a one-off window with If-Match and local times', async () => {
    const user = userEvent.setup();
    let put: { body: unknown; ifMatch: string | null } | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/maintenance-windows': { windows: [dbUpgrade] },
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'db-1' }], next_cursor: '' },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'PUT /api/v1/orgs/acme/maintenance-windows/w2': (req: MockRequest) => {
        put = { body: req.body, ifMatch: req.headers.get('If-Match') };
        return { window: dbUpgrade };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Edit db upgrade' }));
    const dialog = screen.getByRole('dialog', { name: 'Edit db upgrade' });
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put?.ifMatch).toBe('"3"'));
    expect(put?.body).toEqual({
      name: 'db upgrade', target_kind: 'host', target_id: 'h1', note: 'kernel', reason: 'maintenance',
      starts_at: '2026-09-22T10:00:00.000Z', ends_at: '2026-09-22T12:00:00.000Z',
    });
  });

  // v0.4a final review, Important. Decision 3 claims silencing "reuses the
  // API and GUI"; it reused the API only. body() omitted `reason`,
  // MaintenanceWindowBody.Reason is omitempty, and the service defaults an
  // empty one to `maintenance` — so the Save button on an unchanged
  // silence window converted it back, un-muting every alert it covered and
  // newly pausing that host's scheduled runs, invisibly.
  it('preserves reason=silence through an unchanged edit', async () => {
    const user = userEvent.setup();
    const silence: MaintenanceWindow = { ...dbUpgrade, id: 'w3', name: 'mute disk', reason: 'silence', revision: 7 };
    let put: { body: Record<string, unknown> } | undefined;
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/maintenance-windows': { windows: [silence] },
      'GET /api/v1/orgs/acme/hosts': { hosts: [{ id: 'h1', name: 'db-1' }], next_cursor: '' },
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'PUT /api/v1/orgs/acme/maintenance-windows/w3': (req: MockRequest) => {
        put = { body: req.body as Record<string, unknown> };
        return { window: silence };
      },
    });
    renderPage();
    await user.click(await screen.findByRole('button', { name: 'Edit mute disk' }));
    const dialog = screen.getByRole('dialog', { name: 'Edit mute disk' });
    expect(within(dialog).getByLabelText('Why')).toHaveValue('Silence — do not raise alerts; scheduled runs are unaffected');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(put?.body.reason).toBe('silence'));
  });

  // The other half: the reason has to be settable and visible, or decision
  // 3's "reuses the GUI" stays false in the other direction — a window
  // created here could only ever be a maintenance one, and the list could
  // not tell the two apart.
  it('creates a silence window and labels it in the list', async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | undefined;
    const silence: MaintenanceWindow = { ...dbUpgrade, id: 'w4', name: 'mute disk', reason: 'silence' };
    mockApi({
      'GET /api/v1/orgs/acme': org('operator'),
      'GET /api/v1/orgs/acme/maintenance-windows': { windows: [silence] },
      'GET /api/v1/orgs/acme/hosts': noHosts,
      'GET /api/v1/orgs/acme/inventory/groups': { groups: [] },
      'POST /api/v1/orgs/acme/maintenance-windows': (req: MockRequest) => {
        posted = req.body as Record<string, unknown>;
        return { window: silence };
      },
    });
    renderPage();

    const row = (await screen.findByText('mute disk')).closest('[data-row]')!.parentElement as HTMLElement;
    expect(within(row).getByText('silence')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'New window' }));
    const dialog = screen.getByRole('dialog', { name: 'New window' });
    await user.type(within(dialog).getByLabelText('Name'), 'mute it');
    await pickOption(within(dialog), 'Why', { value: 'silence' });
    await user.type(within(dialog).getByLabelText('From'), '2026-09-22T10:00');
    await user.type(within(dialog).getByLabelText('Until'), '2026-09-22T12:00');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(posted?.reason).toBe('silence'));
  });
});

describe('maintenance time helpers', () => {
  it('round-trips local inputs and labels windows', () => {
    expect(fromLocalInput(toLocalInput('2026-09-22T10:00:00Z'))).toBe('2026-09-22T10:00:00.000Z');
    expect(toLocalInput(null)).toBe('');
    expect(targetLabel({ ...dbUpgrade, target_kind: 'inventory_group', target_name: 'pg' }, en)).toBe('Group pg');
    expect(whenLabel({ ...nightly, description: '' }, en, formatter('en'))).toBe('0 2 * * 6 · for 2h · Europe/Berlin');
    expect(whenLabel(dbUpgrade, en, formatter('en'))).toContain('–');
  });
});
