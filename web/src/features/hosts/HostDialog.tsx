import { useMemo, useState } from 'react';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm, useWatch } from 'react-hook-form';
import { X } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Switch } from '../../ui/switch.tsx';
import { Tag } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { ApiError } from '../../api/client.ts';
import { useCreateHost, useReloadHost, useUpdateHost, useZones } from '../../api/hooks/useHosts.ts';
import { useBastions, useHostSSH } from '../../api/hooks/useSsh.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useHostInventoryPreview } from '../../api/hooks/useInventory.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useHostTags } from '../../api/hooks/useTags.ts';
import type { Bastion, Credential, Host, HostInput } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { schemas, type HostFormValues } from '../../lib/schemas.ts';
import { useT } from '../../i18n/useT.ts';
import { BastionDialog } from '../credentials/BastionDialog.tsx';
import { parseTagChip, type ParsedTag } from './selectorInput.ts';
import { manualTagPairs } from './tags.ts';
import { ZoneDialog } from './ZoneDialog.tsx';

/** The two jump choices that are not a jump host. A bastion id is a uuid,
 * so neither can collide with one. */
const INHERIT = 'inherit';
const DIRECT = 'none';

export interface HostDialogProps {
  org: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Edit this host; add a new one when unset. */
  host?: Host;
}

/**
 * HostDialog adds a host or edits one — the same fields either way: name,
 * address, zone, jump host, tags and the two capabilities. The jump host
 * and the tags travel in the same request as the host itself, so a new host
 * is never saved half-configured.
 *
 * The body is mounted only while the dialog is open (ui/dialog.tsx keeps its
 * children mounted), so every opening starts from a fresh form and no query
 * below runs while it is closed.
 */
export function HostDialog({ org, open, onOpenChange, host }: HostDialogProps) {
  const t = useT();
  const close = () => onOpenChange(false);
  return (
    <Dialog open={open} onClose={close} title={host ? t.hosts.dialog.titleEdit(host.name) : t.hosts.dialog.titleAdd}>
      {open && <HostDialogLoader org={org} host={host} onClose={close} />}
    </Dialog>
  );
}

/** HostDialogLoader gathers what the form pre-fills from — the caller's role
 * (it decides whether the jump host is offered at all), and, when editing,
 * the host's tags and SSH settings — and mounts the form once it has them,
 * so the form's initial values are never a half-loaded guess. */
function HostDialogLoader({ org, host, onClose }: { org: string; host?: Host; onClose: () => void }) {
  const t = useT();
  const orgQuery = useOrg(org);
  const role = orgQuery.data?.org.role;
  const canJump = may(role, 'inventory.write');
  const canCreateBastion = may(role, 'credentials.write');
  const hostId = host?.id ?? '';
  const editId = canJump ? hostId : '';

  const zones = useZones(org);
  const bastions = useBastions(org);
  const credentials = useCredentials(org, { enabled: canCreateBastion });
  const tags = useHostTags(org, hostId);
  const ssh = useHostSSH(org, editId);
  const preview = useHostInventoryPreview(org, editId);
  const reload = useReloadHost(org, hostId);

  // The host the form edits, and the key that remounts the form onto a
  // reloaded one. A stale host is replaced, never merged into.
  const [base, setBase] = useState(host);
  const [formKey, setFormKey] = useState(0);

  async function onReload(): Promise<void> {
    setBase(await reload());
    setFormKey((k) => k + 1);
  }

  // orgQuery's failure is not fatal: no role means no jump host field, and
  // the server still has the last word on every write.
  const pending =
    orgQuery.isPending || zones.isPending || (!!host && (tags.isPending || (canJump && ssh.isPending)));
  if (pending) return <p className="text-xs text-muted-foreground">{t.common.loading}</p>;
  if (zones.isError || (host && (tags.isError || (canJump && ssh.isError)))) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {t.hosts.dialog.loadFailed}
      </p>
    );
  }

  const manualTags = manualTagPairs(tags.data?.tags);
  const sshInitial = ssh.data?.ssh;
  const initialJump = sshInitial?.bastion_mode === 'explicit' && sshInitial.bastion_id ? sshInitial.bastion_id : (sshInitial?.bastion_mode ?? INHERIT);

  return (
    <HostForm
      key={formKey}
      org={org}
      host={base}
      onClose={onClose}
      onReload={onReload}
      canJump={canJump}
      canCreateBastion={canCreateBastion}
      zones={zones.data?.zones ?? []}
      bastions={bastions.data?.bastions ?? []}
      credentials={credentials.data?.credentials ?? []}
      initialTags={manualTags}
      initialJump={initialJump}
      // A connection with a null chain is a resolved direct route, not an unknown one.
      resolvedChain={preview.data?.connection ? (preview.data.connection.bastions ?? []) : undefined}
    />
  );
}

interface HostFormProps {
  org: string;
  host?: Host;
  onClose: () => void;
  onReload: () => Promise<void>;
  canJump: boolean;
  canCreateBastion: boolean;
  zones: readonly { id: string; slug: string; name: string; bastion_id?: string }[];
  bastions: readonly Bastion[];
  credentials: readonly Credential[];
  initialTags: ParsedTag[];
  initialJump: string;
  /** The host's resolved jump chain from the inventory preview, when known. */
  resolvedChain?: string[];
}

function tagsKey(tags: readonly ParsedTag[]): string {
  return JSON.stringify([...tags].map((tag) => [tag.key, tag.value]).sort());
}

function HostForm({ org, host, onClose, onReload, canJump, canCreateBastion, zones, bastions, credentials, initialTags, initialJump, resolvedChain }: HostFormProps) {
  const t = useT();
  const toast = useToast();
  const isEdit = !!host;
  const create = useCreateHost(org);
  const update = useUpdateHost(org, host?.id ?? '');

  // What the form opened with, kept apart from live query data: "changed"
  // means changed from what the person saw, not from whatever a background
  // refetch brought in since. The resolved chain is deliberately not in it:
  // it only feeds a label, and the inventory preview may well land after the
  // form has mounted.
  const [initial] = useState({ tags: initialTags, jump: initialJump, zoneId: host?.zone_id ?? '' });

  const { hostFormSchema } = useMemo(() => schemas(t), [t]);
  const form = useForm<HostFormValues>({
    resolver: zodResolver(hostFormSchema),
    defaultValues: {
      name: host?.name ?? '',
      zone_id: host?.zone_id ?? '',
      address: host?.address ?? '',
      managed: host?.managed ?? false,
      monitored: host?.monitored ?? false,
    },
  });
  const zoneId = useWatch({ control: form.control, name: 'zone_id' });

  const [jump, setJump] = useState(initialJump);
  const [tags, setTags] = useState<ParsedTag[]>(initialTags);
  const [tagInput, setTagInput] = useState('');
  const [tagError, setTagError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [stale, setStale] = useState(false);
  const [reloading, setReloading] = useState(false);
  // null = closed; a string is the name typed into the picker before it
  // opened the dialog, as ZoneDialog/BastionDialog's initialName expects.
  const [newZoneName, setNewZoneName] = useState<string | null>(null);
  const [newBastionName, setNewBastionName] = useState<string | null>(null);

  const bastionName = (id: string) => bastions.find((b) => b.id === id)?.name ?? id.slice(0, 8);
  const zone = zones.find((z) => z.id === zoneId);
  const zoneChanged = isEdit && zoneId !== initial.zoneId;

  // What "inherited" resolves to. While the zone is the host's own, and the
  // host does inherit today, the inventory preview has already worked it
  // out (groups included). Otherwise only the zone's default is known, and
  // an inventory group may still override it.
  const useResolved = isEdit && !zoneChanged && initial.jump === INHERIT && resolvedChain !== undefined;
  let inheritedLabel = t.hosts.dialog.jumpInheritedBare;
  if (useResolved) {
    inheritedLabel = t.hosts.dialog.jumpInherited(resolvedChain.length > 0 ? resolvedChain.map(bastionName).join(' → ') : t.hosts.dialog.jumpNone);
  } else if (zone) {
    inheritedLabel = t.hosts.dialog.jumpInherited(zone.bastion_id ? bastionName(zone.bastion_id) : t.hosts.dialog.jumpNone);
  }
  const showInheritHint = jump === INHERIT && !useResolved;

  function addTag(): boolean {
    const text = tagInput.trim();
    if (text === '') return true;
    const parsed = parseTagChip(text, t);
    if (!parsed.ok) {
      setTagError(parsed.error);
      return false;
    }
    setTags((current) => [...current.filter((tag) => tag.key !== parsed.tag.key), parsed.tag]);
    setTagInput('');
    setTagError(null);
    return true;
  }

  const submit = form.handleSubmit(async (values) => {
    setError(null);
    // A chip still in the field is part of what was typed, not lost to a
    // missing Enter.
    let finalTags = tags;
    const pendingTag = tagInput.trim();
    if (pendingTag !== '') {
      const parsed = parseTagChip(pendingTag, t);
      if (!parsed.ok) {
        setTagError(parsed.error);
        return;
      }
      finalTags = [...tags.filter((tag) => tag.key !== parsed.tag.key), parsed.tag];
    }

    const body: HostInput = { name: values.name, zone_id: values.zone_id, address: values.address, managed: values.managed, monitored: values.monitored };
    // Sent only when it differs from what the host already has (on create:
    // from the default), so an untouched field is left alone server-side —
    // and a role without inventory.write never sends the field at all.
    if (canJump && jump !== initial.jump) {
      body.jump = jump === INHERIT || jump === DIRECT ? { mode: jump } : { mode: 'explicit', bastion_id: jump };
    }
    if (tagsKey(finalTags) !== tagsKey(initial.tags)) body.tags = finalTags;

    try {
      if (host) {
        await update.mutateAsync({ ...body, revision: host.revision });
        toast.show(t.hosts.dialog.saved(values.name));
      } else {
        await create.mutateAsync(body);
        toast.show(t.hosts.dialog.created(values.name));
      }
      onClose();
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        setStale(true);
        return;
      }
      setError(err instanceof Error ? err.message : isEdit ? t.hosts.dialog.saveFailed : t.hosts.dialog.createFailed);
    }
  });

  async function reload() {
    setReloading(true);
    try {
      await onReload();
    } catch {
      setReloading(false);
      setError(t.hosts.dialog.reloadFailed);
    }
  }

  const errors = form.formState.errors;
  const pending = create.isPending || update.isPending;

  return (
    <>
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="host_name">{t.hosts.dialog.name}</Label>
          <Input id="host_name" {...form.register('name')} placeholder={t.hosts.dialog.namePlaceholder} />
          {errors.name && <span className="text-[11px] text-destructive">{errors.name.message}</span>}
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="host_address">{t.hosts.dialog.address}</Label>
          <Input id="host_address" {...form.register('address')} placeholder={t.hosts.dialog.addressPlaceholder} />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="host_zone">{t.hosts.dialog.zone}</Label>
          <Controller
            control={form.control}
            name="zone_id"
            render={({ field }) => (
              <Select
                id="host_zone"
                options={zones.map((z) => ({ value: z.id, label: z.name }))}
                value={field.value}
                onChange={field.onChange}
                placeholder={t.hosts.dialog.selectZone}
                create={{ label: t.hosts.dialog.newZone, onSelect: setNewZoneName }}
              />
            )}
          />
          {errors.zone_id && <span className="text-[11px] text-destructive">{errors.zone_id.message}</span>}
          {zoneChanged && zone && <span className="text-[11px] text-muted-foreground">{t.hosts.dialog.zoneChanged(zone.slug)}</span>}
        </div>
        {canJump && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="host_jump">{t.hosts.dialog.jump}</Label>
            <Select
              id="host_jump"
              options={[
                { value: INHERIT, label: inheritedLabel },
                { value: DIRECT, label: t.hosts.dialog.jumpDirect },
                ...bastions.map((b) => ({ value: b.id, label: b.name })),
              ]}
              value={jump}
              onChange={setJump}
              create={canCreateBastion ? { label: t.hosts.dialog.jumpCreate, onSelect: setNewBastionName } : undefined}
            />
            {showInheritHint && <span className="text-[11px] text-muted-foreground">{t.hosts.dialog.jumpInheritedHint}</span>}
          </div>
        )}
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="host_tags">{t.hosts.dialog.tags}</Label>
          {tags.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {tags.map((tag) => (
                <Tag key={tag.key}>
                  {tag.key}={tag.value}
                  <button
                    type="button"
                    className="ml-1 inline-flex items-center"
                    aria-label={t.hosts.detail.removeTag(tag.key)}
                    onClick={() => setTags((current) => current.filter((c) => c.key !== tag.key))}
                  >
                    <X size={11} />
                  </button>
                </Tag>
              ))}
            </div>
          )}
          <div className="flex items-center gap-2">
            <Input
              id="host_tags"
              value={tagInput}
              placeholder={t.hosts.detail.tagPlaceholder}
              onChange={(e) => {
                setTagInput(e.target.value);
                setTagError(null);
              }}
              // Enter adds the chip; it must not submit the whole form.
              onKeyDown={(e) => {
                if (e.key !== 'Enter') return;
                e.preventDefault();
                addTag();
              }}
            />
            <Button type="button" onClick={() => addTag()}>
              {t.hosts.detail.addTag}
            </Button>
          </div>
          {tagError && <span className="text-[11px] text-destructive">{tagError}</span>}
        </div>
        <Controller
          control={form.control}
          name="managed"
          render={({ field }) => (
            <CapabilityRow id="host_managed" label={t.hosts.capability.managedLong} hint={t.hosts.dialog.managedHint} checked={field.value} onChange={field.onChange} />
          )}
        />
        <Controller
          control={form.control}
          name="monitored"
          render={({ field }) => (
            <CapabilityRow id="host_monitored" label={t.hosts.capability.monitored} hint={t.hosts.dialog.monitoredHint} checked={field.value} onChange={field.onChange} />
          )}
        />
        {stale && (
          <div role="alert" className="flex items-center justify-between gap-3 rounded-md border border-border bg-background p-3 text-xs text-destructive">
            <span>{t.hosts.dialog.stale}</span>
            <Button type="button" onClick={() => void reload()} disabled={reloading}>
              {t.hosts.dialog.reload}
            </Button>
          </div>
        )}
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={pending}>
            {isEdit ? t.hosts.dialog.save : t.hosts.dialog.create}
          </Button>
        </div>
      </form>
      {/* Siblings of the <form>, not inside it: ui/dialog.tsx does not
          portal, so a <dialog> nested in the form would have its own submit
          bubble up into this form's onSubmit. */}
      {newZoneName !== null && (
        <ZoneDialog
          org={org}
          open
          initialName={newZoneName}
          onClose={() => setNewZoneName(null)}
          onCreated={(id) => form.setValue('zone_id', id, { shouldValidate: true })}
        />
      )}
      {newBastionName !== null && (
        <BastionDialog
          org={org}
          bastions={bastions}
          credentials={credentials}
          canCreateCredential={canCreateBastion}
          initialName={newBastionName}
          onClose={() => setNewBastionName(null)}
          onCreated={setJump}
        />
      )}
    </>
  );
}

function CapabilityRow({ id, label, hint, checked, onChange }: { id: string; label: string; hint: string; checked: boolean; onChange: (checked: boolean) => void }) {
  return (
    <div className="flex items-start gap-3">
      <Switch id={id} checked={checked} onCheckedChange={onChange} aria-describedby={`${id}_hint`} />
      <div className="flex flex-col gap-0.5">
        <label htmlFor={id} className="text-xs font-medium">
          {label}
        </label>
        <span id={`${id}_hint`} className="text-[11px] text-muted-foreground">
          {hint}
        </span>
      </div>
    </div>
  );
}
