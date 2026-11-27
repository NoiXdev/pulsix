import { useState, type FormEvent } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Switch } from '../../ui/switch.tsx';
import { Select } from '../../ui/Select.tsx';
import { useCreateChannel, useUpdateChannel } from '../../api/hooks/useNotifications.ts';
import type { NotifyChannel, NotifyChannelInput } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { PresetFields } from './PresetFields.tsx';
import { BUILT_URL, SECRET_NAMES, initialSettings, settingsBody, type Preset, type SecretName } from './presetSettings.ts';

const TEXTAREA = 'min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

/** Every format, in the order the select lists them. */
const PRESETS: readonly Preset[] = ['', 'slack', 'mattermost', 'telegram', 'ntfy', 'pushover', 'twilio', 'pagerduty'];

/** The shape of each service's URL, as the URL field's placeholder (none
 * where Pulsix builds the URL). */
const URL_SHAPES: Partial<Record<Preset, string>> = {
  '': 'https://hooks.example.com/…',
  slack: 'https://hooks.slack.com/services/…',
  mattermost: 'https://mattermost.example.com/hooks/…',
  telegram: 'https://api.telegram.org/bot<token>/sendMessage',
  ntfy: 'https://ntfy.sh/<topic>',
};

interface HeaderRow {
  name: string;
  /** Empty while editing keeps the stored value. */
  value: string;
}

export interface ChannelDialogProps {
  org: string;
  /** The channel to edit; undefined creates one. */
  channel?: NotifyChannel;
  mailAvailable: boolean;
  onClose: () => void;
  /** Called with the new channel's id, only on a create (never an edit) —
   * the quick-create row (v0.4g) uses it to select what it just made. */
  onCreated: (id: string) => void;
  /** Prefills the name, from what had been typed into the picker. Same
   * prop shape BastionDialog already has (v0.4g Task 5). */
  initialName?: string;
}

/** ChannelDialog creates or edits a notification channel. Stored secrets
 * (the webhook URL, header values, the signing secret) are never shown:
 * leaving their fields empty while editing keeps them. */
export function ChannelDialog({ org, channel, mailAvailable, onClose, onCreated, initialName }: ChannelDialogProps) {
  const t = useT();
  const m = t.notifications;
  const d = m.channelDialog;
  const editing = !!channel;
  const create = useCreateChannel(org);
  const update = useUpdateChannel(org, channel?.id ?? '');
  const [kind, setKind] = useState<'email' | 'webhook'>(channel?.kind ?? (mailAvailable ? 'email' : 'webhook'));
  const [name, setName] = useState(channel?.name ?? initialName ?? '');
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [recipients, setRecipients] = useState((channel?.email?.recipients ?? []).join('\n'));
  const [method, setMethod] = useState<'POST' | 'PUT'>(channel?.webhook?.method ?? 'POST');
  const [url, setUrl] = useState('');
  const [headers, setHeaders] = useState<HeaderRow[]>((channel?.webhook?.headers ?? []).map((h) => ({ name: h, value: '' })));
  const [template, setTemplate] = useState(channel?.webhook?.body_template ?? '');
  const [signed, setSigned] = useState(channel?.webhook?.signed ?? false);
  const [secret, setSecret] = useState('');
  const [preset, setPreset] = useState<Preset>(channel?.webhook?.preset ?? '');
  const [chatId, setChatId] = useState(channel?.webhook?.chat_id ?? '');
  const [settings, setSettings] = useState(() => initialSettings(channel));
  const [secrets, setSecrets] = useState<Partial<Record<SecretName, string>>>({});
  const [error, setError] = useState<string | null>(null);
  // A preset owns the body (backlog 4.7): no method choice, no template.
  // Changing it while editing needs the URL again, since the stored one
  // belongs to the old service and is never shown.
  const presetChanged = editing && preset !== (channel.webhook?.preset ?? '');
  // Pushover, Twilio and PagerDuty have no URL to enter (backlog 4.8).
  const builtUrl = BUILT_URL.has(preset);
  const urlRequired = !builtUrl && (!editing || presetChanged);
  // A preset's stored secrets are kept while it stays the same, and for
  // Twilio while the account stays the same: its token belongs to it.
  const accountChanged = preset === 'twilio' && settings.accountSid.trim() !== (channel?.webhook?.account_sid ?? '');
  const storedSecrets = editing && !presetChanged && !accountChanged ? (channel.webhook?.secrets ?? []) : [];
  const typedSecrets = Object.fromEntries((SECRET_NAMES[preset] ?? []).filter((n) => (secrets[n] ?? '') !== '').map((n) => [n, secrets[n] as string]));
  // A preset sends its own Content-Type (review M2): such a row stays in
  // the form, so switching back to a generic webhook keeps it, but is not
  // sent while a preset is chosen.
  const ownedByPreset = (h: HeaderRow) => preset !== '' && h.name.trim().toLowerCase() === 'content-type';
  const dropsContentType = headers.some(ownedByPreset);

  function body(): NotifyChannelInput {
    if (kind === 'email') {
      return { kind, name, enabled, email: { recipients: recipients.split(/[\n,]/).map((r) => r.trim()).filter(Boolean) } };
    }
    return {
      kind,
      name,
      enabled,
      webhook: {
        method: preset !== '' ? 'POST' : method,
        ...(url.trim() !== '' && !builtUrl ? { url: url.trim() } : {}),
        headers: headers.filter((h) => h.name.trim() !== '' && !ownedByPreset(h)).map((h) => (h.value !== '' ? { name: h.name.trim(), value: h.value } : { name: h.name.trim() })),
        ...(preset === '' ? { body_template: template } : {}),
        signed,
        ...(signed && secret !== '' ? { signing_secret: secret } : {}),
        ...(preset !== '' ? { preset } : {}),
        ...(preset === 'telegram' ? { chat_id: chatId.trim() } : {}),
        ...settingsBody(preset, settings),
        ...(Object.keys(typedSecrets).length > 0 ? { secrets: typedSecrets } : {}),
      },
    };
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      if (channel) {
        await update.mutateAsync({ ...body(), revision: channel.revision });
      } else {
        const res = await create.mutateAsync(body());
        onCreated(res.channel.id);
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : m.savingFailed);
    }
  }

  const keep = editing && !presetChanged ? d.keep : undefined;
  return (
    <Dialog open onClose={onClose} title={editing ? d.editTitle(channel.name) : d.addTitle} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="ch-kind">{d.kind}</Label>
        <Select
          id="ch-kind"
          options={[
            {
              value: 'email',
              label: mailAvailable ? m.channels.email : d.emailUnavailable,
              disabled: !mailAvailable && !editing,
            },
            { value: 'webhook', label: m.channels.webhook },
          ]}
          value={kind}
          onChange={(v) => setKind(v as 'email' | 'webhook')}
          disabled={editing}
        />
        <Label htmlFor="ch-name">{m.name}</Label>
        <Input id="ch-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        <div className="flex items-center gap-2 text-xs">
          <Switch id="ch-enabled" checked={enabled} onCheckedChange={setEnabled} />
          <label htmlFor="ch-enabled">{m.enabledLabel}</label>
        </div>
        {kind === 'email' ? (
          <>
            <Label htmlFor="ch-recipients">{d.recipients}</Label>
            <textarea id="ch-recipients" className={TEXTAREA} value={recipients} onChange={(e) => setRecipients(e.target.value)} placeholder="ops@example.com" />
          </>
        ) : (
          <>
            <Label htmlFor="ch-preset">{d.format}</Label>
            <Select
              id="ch-preset"
              options={PRESETS.map((p) => ({ value: p, label: d.formats[p === '' ? 'raw' : p] }))}
              value={preset}
              onChange={(v) => setPreset(v as Preset)}
            />
            <div className={preset === '' ? 'grid grid-cols-[100px_minmax(0,1fr)] gap-2' : 'flex flex-col'}>
              {preset === '' && (
                <div className="flex flex-col gap-1">
                  <Label htmlFor="ch-method">{d.method}</Label>
                  <Select
                    id="ch-method"
                    options={[
                      { value: 'POST', label: 'POST' },
                      { value: 'PUT', label: 'PUT' },
                    ]}
                    value={method}
                    onChange={(v) => setMethod(v as 'POST' | 'PUT')}
                  />
                </div>
              )}
              {!builtUrl && (
                <div className="flex flex-col gap-1">
                  <Label htmlFor="ch-url">{editing && !presetChanged ? d.urlNow(channel.webhook?.url ?? '') : d.url}</Label>
                  <Input id="ch-url" className="font-mono" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={keep ?? URL_SHAPES[preset]} required={urlRequired} />
                </div>
              )}
            </div>
            {presetChanged && !builtUrl && <p className="text-[11px] text-muted-foreground">{d.formatUrlAgain}</p>}
            {preset === 'telegram' && (
              <>
                <p className="text-[11px] text-muted-foreground">{d.telegramToken}</p>
                <Label htmlFor="ch-chat">{d.chatId}</Label>
                <Input id="ch-chat" className="font-mono" value={chatId} onChange={(e) => setChatId(e.target.value)} placeholder={d.chatIdPlaceholder} required maxLength={64} />
              </>
            )}
            <PresetFields
              preset={preset}
              settings={settings}
              onSettings={setSettings}
              secrets={secrets}
              onSecret={(name, value) => setSecrets({ ...secrets, [name]: value })}
              stored={storedSecrets}
              d={d}
            />
            <span className="text-[11px] font-medium text-muted-foreground">{d.headers}</span>
            {headers.map((h, i) => (
              <div key={i} className="grid grid-cols-[minmax(0,0.8fr)_minmax(0,1fr)_32px] gap-2">
                <Input aria-label={d.headerName(i + 1)} className="font-mono" value={h.name} onChange={(e) => setHeaders(headers.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} />
                <Input
                  aria-label={d.headerValue(i + 1)}
                  type="password"
                  value={h.value}
                  placeholder={keep}
                  onChange={(e) => setHeaders(headers.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                />
                <Button type="button" variant="ghost" aria-label={d.removeHeader(i + 1)} onClick={() => setHeaders(headers.filter((_, j) => j !== i))}>
                  <Trash2 size={13} />
                </Button>
              </div>
            ))}
            {dropsContentType && <p className="text-[11px] text-muted-foreground">{d.contentTypeByFormat}</p>}
            <Button type="button" variant="ghost" className="self-start" onClick={() => setHeaders([...headers, { name: '', value: '' }])}>
              <Plus size={13} /> {d.addHeader}
            </Button>
            {preset === '' && (
              <>
                <Label htmlFor="ch-template">{d.bodyTemplate}</Label>
                <textarea id="ch-template" className={TEXTAREA} value={template} onChange={(e) => setTemplate(e.target.value)} placeholder='{"text": {{ schedule.name | json }}}' />
              </>
            )}
            <div className="flex items-center gap-2 text-xs">
              <Switch id="ch-signed" checked={signed} onCheckedChange={setSigned} />
              <label htmlFor="ch-signed">{d.sign}</label>
            </div>
            {signed && (
              <>
                <Label htmlFor="ch-secret">{d.signingSecret}</Label>
                <Input id="ch-secret" type="password" value={secret} onChange={(e) => setSecret(e.target.value)} placeholder={channel?.webhook?.signed ? keep : undefined} />
              </>
            )}
          </>
        )}
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            {editing ? t.common.save : d.create}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
