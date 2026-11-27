import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { ChannelDialog } from './ChannelDialog.tsx';
import { channelColumns } from './channelColumns.tsx';
import { en } from '../../i18n/en/index.ts';
import type { NotifyChannel } from '../../api/types.ts';

const slack: NotifyChannel = {
  id: 'c1', kind: 'webhook', name: 'chat', enabled: true, revision: 3, created_at: '', updated_at: '',
  webhook: { method: 'POST', url: 'https://hooks.slack.com/…', headers: [], signed: false, preset: 'slack' },
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

describe('ChannelDialog presets', () => {
  it('a Slack preset hides the method and the body template and sends no template', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'POST /api/v1/orgs/acme/notification-channels': (req: MockRequest) => {
        posted = req.body;
        return { channel: slack };
      },
    });
    const dialog = renderDialog();
    expect(within(dialog).getByLabelText(/Body template/)).toBeInTheDocument();
    await pickOption(within(dialog), 'Format', 'Slack');
    expect(within(dialog).queryByLabelText(/Body template/)).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Method')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Chat ID')).not.toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Name'), 'chat');
    await user.type(within(dialog).getByLabelText('URL'), 'https://hooks.slack.com/services/T0/B0/x');
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }));
    await waitFor(() =>
      expect(posted).toEqual({
        kind: 'webhook', name: 'chat', enabled: true,
        webhook: { method: 'POST', url: 'https://hooks.slack.com/services/T0/B0/x', headers: [], signed: false, preset: 'slack' },
      }),
    );
  });

  it('a Telegram preset asks for the chat id and sends it', async () => {
    const user = userEvent.setup();
    let posted: { webhook?: Record<string, unknown> } | undefined;
    mockApi({
      'POST /api/v1/orgs/acme/notification-channels': (req: MockRequest) => {
        posted = req.body as typeof posted;
        return { channel: slack };
      },
    });
    const dialog = renderDialog();
    await pickOption(within(dialog), 'Format', 'Telegram');
    expect(within(dialog).getByText(/The bot token is part of the URL/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Name'), 'tg');
    await user.type(within(dialog).getByLabelText('URL'), 'https://api.telegram.org/bot1:x/sendMessage');
    await user.type(within(dialog).getByLabelText('Chat ID'), '-1001234567890');
    await user.click(within(dialog).getByRole('button', { name: 'Create channel' }));
    await waitFor(() => expect(posted?.webhook).toMatchObject({ preset: 'telegram', chat_id: '-1001234567890' }));
    expect(posted?.webhook).not.toHaveProperty('body_template');
  });

  it('editing keeps the stored URL until the format changes, then requires it', async () => {
    const user = userEvent.setup();
    let put: unknown;
    mockApi({
      'PUT /api/v1/orgs/acme/notification-channels/c1': (req: MockRequest) => {
        put = req.body;
        return { channel: slack };
      },
    });
    const dialog = renderDialog(slack);
    const url = within(dialog).getByLabelText(/^URL/);
    expect(url).not.toBeRequired();
    await pickOption(within(dialog), 'Format', 'Mattermost');
    expect(within(dialog).getByLabelText(/^URL/)).toBeRequired();
    expect(within(dialog).getByText('Changing the format needs the URL again.')).toBeInTheDocument();
    await pickOption(within(dialog), 'Format', 'Slack');
    expect(within(dialog).getByLabelText(/^URL/)).not.toBeRequired();
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(put).toEqual({
        kind: 'webhook', name: 'chat', enabled: true,
        webhook: { method: 'POST', headers: [], signed: false, preset: 'slack' },
      }),
    );
  });

  it('speaks German', async () => {
    const dialog = renderDialog(undefined, 'de');
    await pickOption(within(dialog), 'Format', 'Telegram');
    expect(within(dialog).getByLabelText('Chat-ID')).toBeInTheDocument();
  });
});

describe('channelColumns', () => {
  it('names the preset in the kind column', () => {
    const kind = channelColumns(en).find((c) => c.id === 'kind')!;
    render(<>{kind.render(slack, { density: 'comfortable' })}</>);
    expect(screen.getByText('Webhook · Slack')).toBeInTheDocument();
  });
});
