import { useState, type FormEvent } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useCreateChannel, useUpdateChannel } from '../../api/hooks/useNotifications.ts';
import type { NotifyChannel, NotifyChannelInput } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

const TEXTAREA = 'min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

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
  const [error, setError] = useState<string | null>(null);

  function body(): NotifyChannelInput {
    if (kind === 'email') {
      return { kind, name, enabled, email: { recipients: recipients.split(/[\n,]/).map((r) => r.trim()).filter(Boolean) } };
    }
    return {
      kind,
      name,
      enabled,
      webhook: {
        method,
        ...(url.trim() !== '' ? { url: url.trim() } : {}),
        headers: headers.filter((h) => h.name.trim() !== '').map((h) => (h.value !== '' ? { name: h.name.trim(), value: h.value } : { name: h.name.trim() })),
        body_template: template,
        signed,
        ...(signed && secret !== '' ? { signing_secret: secret } : {}),
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

  const keep = editing ? d.keep : undefined;
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
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          {m.enabledLabel}
        </label>
        {kind === 'email' ? (
          <>
            <Label htmlFor="ch-recipients">{d.recipients}</Label>
            <textarea id="ch-recipients" className={TEXTAREA} value={recipients} onChange={(e) => setRecipients(e.target.value)} placeholder="ops@example.com" />
          </>
        ) : (
          <>
            <div className="grid grid-cols-[100px_minmax(0,1fr)] gap-2">
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
              <div className="flex flex-col gap-1">
                <Label htmlFor="ch-url">{editing ? d.urlNow(channel.webhook?.url ?? '') : d.url}</Label>
                <Input id="ch-url" className="font-mono" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={keep ?? 'https://hooks.example.com/…'} required={!editing} />
              </div>
            </div>
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
            <Button type="button" variant="ghost" className="self-start" onClick={() => setHeaders([...headers, { name: '', value: '' }])}>
              <Plus size={13} /> {d.addHeader}
            </Button>
            <Label htmlFor="ch-template">{d.bodyTemplate}</Label>
            <textarea id="ch-template" className={TEXTAREA} value={template} onChange={(e) => setTemplate(e.target.value)} placeholder='{"text": {{ schedule.name | json }}}' />
            <label className="flex items-center gap-2 text-xs">
              <input type="checkbox" checked={signed} onChange={(e) => setSigned(e.target.checked)} />
              {d.sign}
            </label>
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
