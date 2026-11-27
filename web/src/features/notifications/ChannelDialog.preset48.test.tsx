import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { ChannelDialog } from './ChannelDialog.tsx';
import type { NotifyChannel } from '../../api/types.ts';

const pushover: NotifyChannel = {
  id: 'c1', kind: 'webhook', name: 'phone', enabled: true, revision: 3, created_at: '', updated_at: '',
  webhook: { method: 'POST', url: 'https://api.pushover.net/…', headers: [], signed: false, preset: 'pushover', secrets: ['token', 'user'] },
};

function renderDialog(channel?: NotifyChannel, locale: 'en' | 'de' = 'en') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale={locale}>
        <ChannelDialog org="acme" channel={channel} mailAvailable={false} onClose={vi.fn()} onCreated={vi.fn()} />
      </I18nProvider>
    </QueryClientProvider>,
  );
  return screen.getByRole('dialog');
}

function capture(method: 'POST' | 'PUT') {
  const box: { body?: { webhook?: Record<string, unknown> } } = {};
  const path = method === 'POST' ? 'POST /api/v1/orgs/acme/notification-channels' : 'PUT /api/v1/orgs/acme/notification-channels/c1';
  mockApi({
    [path]: (req: MockRequest) => {
      box.body = req.body as typeof box.body;
      return { channel: pushover };
    },
  });
  return box;
}

describe('ChannelDialog presets of backlog 4.8', () => {
  it('Pushover has no URL field and sends its two secrets', async () => {
    const user = userEvent.setup();
    const sent = capture('POST');
    const dialog = renderDialog();
    await pickOption(within(dialog), 'Format', 'Pushover');
    expect(within(dialog).queryByLabelText('URL')).not.toBeInTheDocument();
    expect(within(dialog).getByText(/no URL to enter/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Name'), 'phone');
    await user.type(within(dialog).getByLabelText('Application token'), 'a'.repeat(30));
    await user.type(within(dialog).getByLabelText('User or group key'), 'u'.repeat(30));
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }));
    await waitFor(() =>
      expect(sent.body?.webhook).toEqual({ method: 'POST', headers: [], signed: false, preset: 'pushover', secrets: { token: 'a'.repeat(30), user: 'u'.repeat(30) } }),
    );
  });

  it('Twilio asks for the account, both numbers and the token, and says it costs', async () => {
    const user = userEvent.setup();
    const sent = capture('POST');
    const dialog = renderDialog();
    await pickOption(within(dialog), 'Format', 'Twilio SMS');
    expect(within(dialog).getByText(/billed to your Twilio account/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Name'), 'sms');
    await user.type(within(dialog).getByLabelText('Account SID'), ' AC0123 ');
    await user.type(within(dialog).getByLabelText('From (number or sender ID)'), '+15005550006');
    await user.type(within(dialog).getByLabelText('To (one number)'), '+4915112345678');
    await user.type(within(dialog).getByLabelText('Auth token'), 't'.repeat(32));
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }));
    await waitFor(() =>
      expect(sent.body?.webhook).toMatchObject({
        preset: 'twilio', account_sid: 'AC0123', from: '+15005550006', to: '+4915112345678', secrets: { auth_token: 't'.repeat(32) },
      }),
    );
    expect(sent.body?.webhook).not.toHaveProperty('url');
  });

  it('PagerDuty sends the region and the integration key', async () => {
    const user = userEvent.setup();
    const sent = capture('POST');
    const dialog = renderDialog();
    await pickOption(within(dialog), 'Format', 'PagerDuty');
    expect(within(dialog).getByText(/change event that pages nobody/)).toBeInTheDocument();
    await pickOption(within(dialog), 'Service region', 'EU');
    await user.type(within(dialog).getByLabelText('Name'), 'pager');
    await user.type(within(dialog).getByLabelText('Integration key'), 'k'.repeat(32));
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }));
    await waitFor(() => expect(sent.body?.webhook).toMatchObject({ preset: 'pagerduty', region: 'eu', secrets: { routing_key: 'k'.repeat(32) } }));
  });

  it('ntfy keeps the URL and hints at the access token header', async () => {
    const user = userEvent.setup();
    const sent = capture('POST');
    const dialog = renderDialog();
    await pickOption(within(dialog), 'Format', 'ntfy');
    expect(within(dialog).getByText(/followed by the access token/)).toBeInTheDocument();
    expect(within(dialog).getByLabelText('URL')).toHaveAttribute('placeholder', 'https://ntfy.sh/<topic>');
    await user.type(within(dialog).getByLabelText('Name'), 'push');
    await user.type(within(dialog).getByLabelText('URL'), 'https://ntfy.sh/pulsix');
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }));
    await waitFor(() => expect(sent.body?.webhook).toMatchObject({ preset: 'ntfy', url: 'https://ntfy.sh/pulsix' }));
    expect(sent.body?.webhook).not.toHaveProperty('secrets');
  });

  it('editing keeps the stored secrets when their fields stay empty', async () => {
    const user = userEvent.setup();
    const sent = capture('PUT');
    const dialog = renderDialog(pushover);
    const token = within(dialog).getByLabelText('Application token');
    expect(token).toHaveAttribute('placeholder', 'leave empty to keep');
    expect(token).not.toBeRequired();
    await user.type(within(dialog).getByLabelText('User or group key'), 'v'.repeat(30));
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(sent.body?.webhook).toMatchObject({ preset: 'pushover', secrets: { user: 'v'.repeat(30) } }));
    expect(sent.body?.webhook).not.toHaveProperty('url');
    expect(sent.body?.webhook?.secrets).not.toHaveProperty('token');
  });

  it('speaks German', async () => {
    const dialog = renderDialog(undefined, 'de');
    await pickOption(within(dialog), 'Format', 'Twilio-SMS');
    expect(within(dialog).getByText(/über das Twilio-Konto abgerechnet/)).toBeInTheDocument();
    expect(within(dialog).getByLabelText('Empfänger (eine Nummer)')).toBeRequired();
  });
});

describe('ChannelDialog Twilio account change (review M2)', () => {
  const sms: NotifyChannel = {
    id: 'c1', kind: 'webhook', name: 'sms', enabled: true, revision: 3, created_at: '', updated_at: '',
    webhook: {
      method: 'POST', url: 'https://api.twilio.com/…', headers: [], signed: false, preset: 'twilio',
      account_sid: 'AC1', from: '+15005550006', to: '+4915112345678', secrets: ['auth_token'],
    },
  };

  it('keeps the auth token for the same account and requires it for another', async () => {
    const user = userEvent.setup();
    renderDialog(sms);
    const dialog = screen.getByRole('dialog');
    const token = within(dialog).getByLabelText('Auth token');
    expect(token).not.toBeRequired();
    expect(token).toHaveAttribute('placeholder', 'leave empty to keep');
    const sid = within(dialog).getByLabelText('Account SID');
    await user.clear(sid);
    await user.type(sid, 'AC2');
    expect(within(dialog).getByLabelText('Auth token')).toBeRequired();
    expect(within(dialog).getByLabelText('Auth token')).not.toHaveAttribute('placeholder');
  });
});
