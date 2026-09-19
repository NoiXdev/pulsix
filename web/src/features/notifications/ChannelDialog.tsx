import { useState, type FormEvent } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useCreateChannel, useUpdateChannel } from '../../api/hooks/useNotifications.ts';
import type { NotifyChannel, NotifyChannelInput } from '../../api/types.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';
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
}

/** ChannelDialog creates or edits a notification channel. Stored secrets
 * (the webhook URL, header values, the signing secret) are never shown:
 * leaving their fields empty while editing keeps them. */
export function ChannelDialog({ org, channel, mailAvailable, onClose }: ChannelDialogProps) {
  const editing = !!channel;
  const create = useCreateChannel(org);
  const update = useUpdateChannel(org, channel?.id ?? '');
  const [kind, setKind] = useState<'email' | 'webhook'>(channel?.kind ?? (mailAvailable ? 'email' : 'webhook'));
  const [name, setName] = useState(channel?.name ?? '');
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
        await create.mutateAsync(body());
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  const keep = editing ? 'leave empty to keep' : undefined;
  return (
    <Dialog open onClose={onClose} title={editing ? `Edit ${channel.name}` : 'Add channel'} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="ch-kind">Kind</Label>
        <select id="ch-kind" className={SELECT} value={kind} disabled={editing} onChange={(e) => setKind(e.target.value as 'email' | 'webhook')}>
          <option value="email" disabled={!mailAvailable && !editing}>
            E-mail{!mailAvailable ? ' (needs outgoing mail settings)' : ''}
          </option>
          <option value="webhook">Webhook</option>
        </select>
        <Label htmlFor="ch-name">Name</Label>
        <Input id="ch-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Enabled
        </label>
        {kind === 'email' ? (
          <>
            <Label htmlFor="ch-recipients">Recipients (one per line)</Label>
            <textarea id="ch-recipients" className={TEXTAREA} value={recipients} onChange={(e) => setRecipients(e.target.value)} placeholder="ops@example.com" />
          </>
        ) : (
          <>
            <div className="grid grid-cols-[100px_minmax(0,1fr)] gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor="ch-method">Method</Label>
                <select id="ch-method" className={SELECT} value={method} onChange={(e) => setMethod(e.target.value as 'POST' | 'PUT')}>
                  <option value="POST">POST</option>
                  <option value="PUT">PUT</option>
                </select>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="ch-url">URL{editing ? ` (now ${channel.webhook?.url})` : ''}</Label>
                <Input id="ch-url" className="font-mono" value={url} onChange={(e) => setUrl(e.target.value)} placeholder={keep ?? 'https://hooks.example.com/…'} required={!editing} />
              </div>
            </div>
            <span className="text-[11px] font-medium text-muted-foreground">Headers</span>
            {headers.map((h, i) => (
              <div key={i} className="grid grid-cols-[minmax(0,0.8fr)_minmax(0,1fr)_32px] gap-2">
                <Input aria-label={`Header ${i + 1} name`} className="font-mono" value={h.name} onChange={(e) => setHeaders(headers.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} />
                <Input
                  aria-label={`Header ${i + 1} value`}
                  type="password"
                  value={h.value}
                  placeholder={keep}
                  onChange={(e) => setHeaders(headers.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                />
                <Button type="button" variant="ghost" aria-label={`Remove header ${i + 1}`} onClick={() => setHeaders(headers.filter((_, j) => j !== i))}>
                  <Trash2 size={13} />
                </Button>
              </div>
            ))}
            <Button type="button" variant="ghost" className="self-start" onClick={() => setHeaders([...headers, { name: '', value: '' }])}>
              <Plus size={13} /> Add header
            </Button>
            <Label htmlFor="ch-template">Body template (empty sends the event as JSON)</Label>
            <textarea id="ch-template" className={TEXTAREA} value={template} onChange={(e) => setTemplate(e.target.value)} placeholder='{"text": {{ schedule.name | json }}}' />
            <label className="flex items-center gap-2 text-xs">
              <input type="checkbox" checked={signed} onChange={(e) => setSigned(e.target.checked)} />
              Sign requests (X-Pulsix-Signature-256)
            </label>
            {signed && (
              <>
                <Label htmlFor="ch-secret">Signing secret (16–256 characters)</Label>
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
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
