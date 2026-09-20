import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ScheduleNotifications } from './ScheduleNotifications.tsx';

const channels = {
  channels: [
    { id: 'c1', kind: 'webhook', name: 'ops hook', enabled: true, revision: 1, created_at: '', updated_at: '' },
    { id: 'c2', kind: 'email', name: 'ops mail', enabled: true, revision: 1, created_at: '', updated_at: '' },
  ],
  mail_available: true,
};
const rules = {
  rules: [
    { on: 'failure', channels: [{ id: 'c1', name: 'ops hook', kind: 'webhook' }, { id: 'c2', name: 'ops mail', kind: 'email' }] },
    { on: 'change', channels: [{ id: 'c1', name: 'ops hook', kind: 'webhook' }] },
  ],
};

function renderIt(canWrite: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <ScheduleNotifications org="acme" scheduleId="s1" canWrite={canWrite} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe('ScheduleNotifications', () => {
  it('shows the rules as text to readers', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/schedules/s1/notifications': rules });
    renderIt(false);
    expect(await screen.findByText('→ ops hook, ops mail')).toBeInTheDocument();
    expect(screen.getByText('on change')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save notifications' })).not.toBeInTheDocument();
  });

  it('edits the rules as a channel × category matrix', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'GET /api/v1/orgs/acme/schedules/s1/notifications': rules,
      'GET /api/v1/orgs/acme/notification-channels': channels,
      'PUT /api/v1/orgs/acme/schedules/s1/notifications': (req: MockRequest) => {
        put = req.body;
        return rules;
      },
    });
    renderIt(true);
    const hookChange = await screen.findByRole('checkbox', { name: 'ops hook on change' });
    expect(hookChange).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'ops mail on failure' })).toBeChecked();
    await user.click(screen.getByRole('checkbox', { name: 'ops mail on failure' }));
    await user.click(screen.getByRole('checkbox', { name: 'ops mail skipped / missed' }));
    await user.click(screen.getByRole('button', { name: 'Save notifications' }));
    await waitFor(() =>
      expect(put).toEqual({
        rules: [
          { on: 'failure', channels: ['c1'] },
          { on: 'change', channels: ['c1'] },
          { on: 'skipped', channels: ['c2'] },
        ],
      }),
    );
    expect(await screen.findByText('Notifications saved')).toBeInTheDocument();
  });

  it('points to the settings when the organization has no channel', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/schedules/s1/notifications': { rules: [] },
      'GET /api/v1/orgs/acme/notification-channels': { channels: [], mail_available: true },
    });
    renderIt(true);
    expect(await screen.findByText(/admins add them under Settings → Notifications/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save notifications' })).toBeDisabled();
  });
});
