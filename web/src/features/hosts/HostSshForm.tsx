import { useRef, useState, type FormEvent, type KeyboardEvent } from 'react';
import { ApiError } from '../../api/client.ts';
import { useUpdateHost } from '../../api/hooks/useHosts.ts';
import { useSetHostSSH } from '../../api/hooks/useSsh.ts';
import type { Bastion, Credential, Host, HostSSH, Zone } from '../../api/types.ts';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useT } from '../../i18n/useT.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { BastionDialog } from '../credentials/BastionDialog.tsx';
import { kindName, routeLabel } from '../credentials/labels.ts';
import { RouteChain } from './RouteChain.tsx';
import { SshDialogTest } from './SshDialogTest.tsx';
import { SshJumpChoices } from './SshJumpChoices.tsx';
import { SshKeySnippets } from './SshKeySnippets.tsx';
import { StaleNotice } from './StaleNotice.tsx';
import { addressChanged, draftOf, draftProblem, draftRoute, effectiveUser, sameDraft, sshBody, sshChanged, sshTarget, type SshDraft } from './sshDraft.ts';
import { useDialogConnectionTest } from './useDialogConnectionTest.ts';
import type { HostRoute } from './useHostRoute.ts';

export interface HostSshFormProps {
  org: string;
  host: Host;
  /** The host's SSH settings as stored (live: a refetch moves it). */
  current: HostSSH;
  bastions: readonly Bastion[];
  credentials: readonly Credential[];
  zones: readonly Zone[];
  /** The route the inventory preview resolved for the stored settings. */
  route?: HostRoute;
  /** hosts.write: the address is a field of the host itself. */
  canEditAddress: boolean;
  /** credentials.write: the create rows of the credential and jump host pickers. */
  canCreateCredential: boolean;
  showSnippets: boolean;
  onClose: () => void;
  onReload: () => Promise<void>;
}

/**
 * HostSshForm is the body of the SSH access dialog: the host's address,
 * port, user, credential and jump host, the route they lead to, the key to
 * install and a connection test. Save writes the address through the host
 * update and the SSH settings through the SSH route — the address first,
 * and each only when it changed — and closes; any failure keeps the dialog
 * open with what went wrong.
 */
export function HostSshForm({
  org, host, current, bastions, credentials, zones, route, canEditAddress, canCreateCredential, showSnippets, onClose, onReload,
}: HostSshFormProps) {
  const t = useT();
  const s = t.hosts.sshDialog;
  const toast = useToast();
  const ask = useConfirm();
  const update = useUpdateHost(org, host.id);
  const saveSsh = useSetHostSSH(org, host.id);
  const test = useDialogConnectionTest(org, host.id);

  // What is stored, live: a save (or a refetch) moves it, and the draft is
  // compared with it — so a save leaves nothing to save.
  const saved = draftOf(host.address, current);
  const [draft, setDraft] = useState<SshDraft>(saved);
  const [error, setError] = useState<string | null>(null);
  const [problem, setProblem] = useState<'port' | 'bastion' | null>(null);
  const [stale, setStale] = useState(false);
  // null = closed; a string is the name typed into the picker before it
  // opened the create dialog (their initialName).
  const [newCredentialName, setNewCredentialName] = useState<string | null>(null);
  const [newBastionName, setNewBastionName] = useState<string | null>(null);

  // A save or a save-and-test in flight: pending is state and lags a
  // double click in the same tick, this does not.
  const busy = useRef(false);

  const dirty = !sameDraft(draft, saved);
  const pending = update.isPending || saveSsh.isPending;
  const set = (patch: Partial<SshDraft>) => {
    setDraft((d) => ({ ...d, ...patch }));
    setProblem(null);
  };

  const credential = credentials.find((c) => c.id === draft.credentialId);
  const zone = zones.find((z) => z.id === host.zone_id);
  const bastionNames = (hops: string[]) => (hops.length > 0 ? hops.join(' → ') : s.noJumpHost);
  const inherited = draftRoute({ mode: 'inherit', bastionId: '', savedMode: current.bastion_mode, resolved: route, zoneBastionId: zone?.bastion_id, bastions });
  const inheritHint = inherited.approximate ? s.inheritZone(bastionNames(inherited.hops)) : s.inheritNow(`${routeLabel(inherited.source, t)} · ${bastionNames(inherited.hops)}`);
  const preview = draftRoute({ mode: draft.mode, bastionId: draft.bastionId, savedMode: current.bastion_mode, resolved: route, zoneBastionId: zone?.bastion_id, bastions });
  const user = effectiveUser(draft.username, credential?.username);
  const target = sshTarget(user, draft.address.trim() || host.name, draft.port.trim() || '22');

  /** save writes what changed; true when everything is stored. */
  async function save(): Promise<boolean> {
    setError(null);
    const found = draftProblem(draft);
    if (found) {
      setProblem(found);
      return false;
    }
    const sent = draft;
    let addressSaved = false;
    try {
      let revision = host.revision;
      let address = saved.address;
      if (addressChanged(sent, saved)) {
        const res = await update.mutateAsync({
          name: host.name, zone_id: host.zone_id, address: sent.address.trim(), managed: host.managed, monitored: host.monitored, revision,
        });
        revision = res.host.revision;
        address = res.host.address;
        addressSaved = true;
      }
      let ssh = current;
      if (sshChanged(sent, saved)) ssh = (await saveSsh.mutateAsync({ body: sshBody(sent), revision })).ssh;
      setDraft(draftOf(address, ssh));
      setStale(false);
      return true;
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) setStale(true);
      else {
        const why = err instanceof Error ? err.message : t.hosts.ssh.saveFailed;
        setError(addressSaved ? s.addressSavedSshFailed(why) : why);
      }
      return false;
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!dirty || pending || busy.current) return;
    busy.current = true;
    try {
      if (await save()) {
        toast.show(t.hosts.ssh.saved);
        onClose();
      }
    } finally {
      busy.current = false;
    }
  }

  async function saveAndTest() {
    if (busy.current) return;
    busy.current = true;
    try {
      if (dirty) {
        if (!(await save())) return;
        toast.show(t.hosts.ssh.saved);
      }
      await test.run();
    } finally {
      busy.current = false;
    }
  }

  async function requestClose() {
    if (!dirty || (await ask({ title: s.discardTitle, message: s.discardBody, confirmLabel: s.discard, destructive: true }))) onClose();
  }

  // Escape asks too while there are changes (or a save is in flight):
  // preventing the keydown's default is what stops the native <dialog>'s
  // close request. A picker that closes its own list on Escape has already
  // prevented it, and that press is left to the picker; so is one inside a
  // create dialog opened from here (React bubbles it through this tree).
  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'Escape' || e.defaultPrevented || (!dirty && !pending)) return;
    if ((e.target as Element).closest('dialog') !== e.currentTarget.closest('dialog')) return;
    e.preventDefault();
    if (!pending) void requestClose();
  }

  async function reload() {
    await onReload();
    setStale(false);
  }

  return (
    <div className="flex flex-col gap-4" onKeyDown={onKeyDown}>
      <form id="ssh-access-form" onSubmit={submit} className="flex flex-col gap-4" noValidate>
        <div className="flex flex-col gap-1">
          <Label htmlFor="ssh-address">{s.address}</Label>
          <Input
            id="ssh-address"
            className="font-mono"
            value={draft.address}
            placeholder={host.name}
            onChange={(e) => set({ address: e.target.value })}
            disabled={!canEditAddress}
            aria-describedby="ssh-address-hint"
          />
          <span id="ssh-address-hint" className="text-[11px] text-muted-foreground">
            {s.addressHint(host.name)}
          </span>
        </div>
        <div className="grid grid-cols-[1fr_110px] gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="ssh-user">{t.hosts.ssh.user}</Label>
            <Input
              id="ssh-user"
              className="font-mono"
              placeholder={credential?.username || t.hosts.ssh.userPlaceholder}
              value={draft.username}
              onChange={(e) => set({ username: e.target.value })}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="ssh-port">{t.hosts.ssh.port}</Label>
            <Input
              id="ssh-port"
              className="font-mono"
              inputMode="numeric"
              value={draft.port}
              onChange={(e) => set({ port: e.target.value })}
              aria-invalid={problem === 'port' || undefined}
            />
            {problem === 'port' && <span className="text-[11px] text-destructive">{s.portInvalid}</span>}
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="ssh-credential">{t.hosts.ssh.credential}</Label>
          <Select
            id="ssh-credential"
            options={credentials.map((c) => ({ value: c.id, label: c.name, hint: kindName(c.kind, t) }))}
            value={draft.credentialId}
            onChange={(credentialId) => set({ credentialId })}
            placeholder={t.hosts.ssh.noCredential}
            create={canCreateCredential ? { label: t.hosts.ssh.newCredential, onSelect: setNewCredentialName } : undefined}
          />
        </div>
        <SshJumpChoices
          mode={draft.mode}
          bastionId={draft.bastionId}
          bastions={bastions}
          inheritHint={inheritHint}
          error={problem === 'bastion' ? s.bastionRequired : undefined}
          disabled={false}
          onMode={(mode) => set({ mode })}
          onBastion={(bastionId) => set({ bastionId })}
          onNewBastion={canCreateCredential ? setNewBastionName : undefined}
        />
        <div className="flex flex-col gap-1 rounded-md bg-background px-3 py-2">
          <span className="text-[11px] font-medium text-muted-foreground">{s.routePreview}</span>
          <RouteChain label={s.routePreview} worker={s.worker(zone?.slug ?? '…')} hops={preview.hops} target={target} />
          <span className="text-[11px] text-muted-foreground">{routeLabel(preview.source, t)}</span>
        </div>
      </form>
      <SshKeySnippets org={org} credential={credential} defaultOpen={showSnippets} />
      <SshDialogTest org={org} hostId={host.id} test={test} dirty={dirty} disabled={pending} onRun={() => void saveAndTest()} />
      {stale && <StaleNotice onReload={reload} />}
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <div className="flex items-center justify-end gap-2 border-t border-border pt-4">
        {dirty && <span className="mr-auto text-[11px] text-[var(--px-warn)]">{s.unsaved}</span>}
        <Button type="button" variant="ghost" onClick={() => void requestClose()} disabled={pending}>
          {t.common.cancel}
        </Button>
        <Button type="submit" form="ssh-access-form" variant="primary" disabled={!dirty || pending}>
          {t.common.save}
        </Button>
      </div>
      {/* Outside the <form>: ui/dialog.tsx does not portal, so a dialog
          nested in this form would submit it with its own. */}
      {newCredentialName !== null && (
        <AddCredentialDialog
          org={org}
          open
          initialName={newCredentialName}
          onClose={() => setNewCredentialName(null)}
          onCreated={(credentialId) => set({ credentialId })}
        />
      )}
      {newBastionName !== null && (
        <BastionDialog
          org={org}
          bastions={bastions}
          credentials={credentials}
          canCreateCredential={canCreateCredential}
          initialName={newBastionName}
          onClose={() => setNewBastionName(null)}
          onCreated={(bastionId) => set({ mode: 'explicit', bastionId })}
        />
      )}
    </div>
  );
}
