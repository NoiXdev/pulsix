import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Switch } from '../../ui/switch.tsx';
import { useT } from '../../i18n/useT.ts';
import { useCreateCheck, useUpdateCheck } from '../../api/hooks/useChecks.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import type { CheckBindingKind, CheckDefinition, CheckDefinitionInput, CheckPlugin, MonitoringGroup } from '../../api/types.ts';

// Still used by HostPicker's own search <input> below, which stays a plain
// text field rather than becoming a Select.
const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground disabled:opacity-50';
const TEXTAREA = 'min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

const PLUGINS: CheckPlugin[] = ['cpu', 'memory', 'disk', 'service'];

/** lines turns a textarea's content into the string list the API takes,
 * dropping blank lines so a trailing newline is not a parameter. */
function lines(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** stringList reads a list parameter back out of a stored definition,
 * whose params are typed as unknown values. */
function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : [];
}

export interface CheckDialogProps {
  org: string;
  /** The definition being edited or copied; undefined creates one. */
  check?: CheckDefinition;
  /** True when `check` is a row being copied rather than edited: the
   * dialog then creates a new, custom definition from its values. */
  copy?: boolean;
  /** The organization's monitoring groups, for a group binding. */
  groups: MonitoringGroup[];
  onClose: () => void;
}

/**
 * CheckDialog creates, edits or copies a check definition.
 *
 * A seeded definition is not editable in place the way a custom one is:
 * the server fixes a seed row's key and plugin (internal/checks
 * UpdateDefinition — "copy it to change them"), so this dialog shows those
 * two read-only and offers Copy instead of sending a value that would come
 * back a 400. Everything else about a seeded row — its interval, its
 * parameters, its binding and whether it is enabled at all — stays
 * editable right here: an operator who cannot retune a seeded check's
 * interval without cloning it is exactly who the spec's "see and edit"
 * decision was written for.
 *
 * A SHARED definition (spec v0.2f decisions 1, 2; migration 00062) is wider
 * than a seeded one, not narrower: key, plugin, interval AND params all
 * belong to the installation-owned definition and are shown read-only
 * (internal/checks refuseSharedDefinitionFieldChange refuses all four), only
 * Copy offered to make an independent custom definition instead — while the
 * binding (org/group/host) and whether it is enabled stay fully editable
 * right here, the OPPOSITE split from a repository or job template, where
 * the binding is what gets disabled.
 */
export function CheckDialog({ org, check, copy = false, groups, onClose }: CheckDialogProps) {
  const t = useT();
  const editing = !!check && !copy;
  const seeded = editing && check.origin === 'seed';
  const shared = editing && check.origin === 'shared';
  // Both seeded and shared rows show key/plugin read-only; a shared row ALSO
  // fixes interval and params, which a seeded row leaves fully editable
  // (checks.Service.UpdateDefinition's own, narrower seed restriction).
  const sharedOrSeeded = seeded || shared;
  const create = useCreateCheck(org);
  const update = useUpdateCheck(org, check?.id ?? '');

  const [plugin, setPlugin] = useState<CheckPlugin>(check?.plugin ?? 'cpu');
  const [key, setKey] = useState(check?.key ?? '');
  const [interval, setInterval] = useState(String(check?.interval_seconds ?? 60));
  const [enabled, setEnabled] = useState(check?.enabled ?? true);
  const [bindingKind, setBindingKind] = useState<CheckBindingKind>(check?.binding_kind ?? 'org');
  const [groupId, setGroupId] = useState(check?.group_id ?? groups[0]?.id ?? '');
  const [hostId, setHostId] = useState(check?.host_id ?? '');
  const [units, setUnits] = useState(stringList(check?.params?.units).join('\n'));
  const [mounts, setMounts] = useState(stringList(check?.params?.mounts).join('\n'));
  const [error, setError] = useState<string | null>(null);

  function params(): Record<string, unknown> {
    if (plugin === 'service') return { units: lines(units) };
    if (plugin === 'disk') {
      const list = lines(mounts);
      return list.length > 0 ? { mounts: list } : {};
    }
    return {};
  }

  function body(): CheckDefinitionInput {
    return {
      plugin,
      // Omitted only on a create, where the server defaults it to the
      // plugin name — the right default for the common
      // single-check-per-plugin case and not this form's business to guess
      // at. On an update the server refuses a blank or absent key outright
      // (internal/checks UpdateDefinition) rather than defaulting it,
      // because there the default is a RENAME of an existing definition,
      // moving the identifier the override precedence keys on. That rule
      // lives there, not here: `required` below is satisfied by a single
      // space, so no form attribute could ever be the guard.
      // key is already trimmed in state (the input trims as it is typed),
      // so this only decides create-time omission.
      ...(key !== '' ? { key } : {}),
      params: params(),
      interval_seconds: Number(interval),
      enabled,
      binding_kind: bindingKind,
      ...(bindingKind === 'group' ? { group_id: groupId } : {}),
      ...(bindingKind === 'host' ? { host_id: hostId } : {}),
    };
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      if (editing) {
        await update.mutateAsync(body());
      } else {
        await create.mutateAsync(body());
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.checks.saveFailed);
    }
  }

  // A group the list does not hold: either the list failed to load or the
  // binding names a group this page has not been told about. Either way the
  // definition's own binding stays selectable and savable.
  const currentGroupMissing = groupId !== '' && !groups.some((g) => g.id === groupId);
  const title = editing ? t.checks.dialog.editTitle(check.key) : copy && check ? t.checks.dialog.copyTitle(check.key) : t.checks.dialog.newTitle;
  return (
    <Dialog open onClose={onClose} title={title} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-plugin">{t.checks.dialog.plugin}</Label>
            <Select
              id="check-plugin"
              options={PLUGINS.map((value) => ({ value, label: t.checks.dialog.plugins[value] }))}
              value={plugin}
              disabled={sharedOrSeeded}
              onChange={(v) => setPlugin(v as CheckPlugin)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-key">{t.checks.dialog.key}</Label>
            <Input
              id="check-key"
              className="font-mono"
              value={key}
              disabled={sharedOrSeeded}
              required={editing}
              // keyPattern, internal/checks/definitions.go — so the
              // refusal arrives before the round trip, and so whitespace
              // (which satisfies `required` on its own) is caught here
              // too. The server rule is still the guard, not this.
              pattern="[a-z0-9][a-z0-9-]{0,62}"
              title={t.checks.dialog.keyTitle}
              placeholder={plugin}
              maxLength={63}
              // Trimmed as it is typed, so `pattern` judges exactly the
              // string body() sends. No valid key contains whitespace at
              // all, and leaving the two out of step made the browser
              // refuse " disk-a" that the server accepts and stores as
              // "disk-a" (review round 3, nit 4).
              onChange={(e) => setKey(e.target.value.trim())}
            />
          </div>
        </div>
        {seeded && (
          <p className="text-[11px] text-muted-foreground">{t.checks.dialog.seededNote}</p>
        )}
        {shared && (
          <p className="text-[11px] text-muted-foreground">{t.checks.dialog.sharedNote}</p>
        )}
        {copy && (
          <p className="text-[11px] text-muted-foreground">{t.checks.dialog.copyNote}</p>
        )}
        {plugin === 'service' && (
          <>
            <Label htmlFor="check-units">{t.checks.dialog.units}</Label>
            <textarea
              id="check-units"
              className={TEXTAREA}
              value={units}
              required
              disabled={shared}
              placeholder="sshd"
              onChange={(e) => setUnits(e.target.value)}
            />
          </>
        )}
        {plugin === 'disk' && (
          <>
            <Label htmlFor="check-mounts">{t.checks.dialog.mounts}</Label>
            <textarea
              id="check-mounts"
              className={TEXTAREA}
              value={mounts}
              disabled={shared}
              placeholder="/&#10;/var"
              onChange={(e) => setMounts(e.target.value)}
            />
          </>
        )}
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-binding">{t.checks.dialog.bindsTo}</Label>
            <Select
              id="check-binding"
              options={[
                { value: 'org', label: t.checks.scope.org },
                { value: 'group', label: groups.length === 0 ? t.checks.scope.groupNone : t.checks.scope.group, disabled: groups.length === 0 },
                { value: 'host', label: t.checks.scope.host },
              ]}
              value={bindingKind}
              onChange={(v) => setBindingKind(v as CheckBindingKind)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-interval">{t.checks.dialog.interval}</Label>
            <Input
              id="check-interval"
              type="number"
              min={10}
              max={86400}
              required
              disabled={shared}
              value={interval}
              onChange={(e) => setInterval(e.target.value)}
            />
          </div>
        </div>
        {bindingKind === 'group' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-group">{t.checks.monitoringGroup}</Label>
            <Select
              id="check-group"
              placeholder={t.checks.chooseGroup}
              required
              value={groupId}
              onChange={setGroupId}
              options={[
                // The binding this definition already has, whenever the
                // list does not contain it — a failed GET
                // /monitoring-groups is enough to empty that list, and
                // without this option Select has nothing to match groupId
                // against and falls back to "<id> (unavailable)" (its own
                // selectedLabel), reading as broken for a binding that is
                // perfectly valid (review finding 1).
                ...(currentGroupMissing ? [{ value: groupId, label: t.checks.groupCurrent(groupId) }] : []),
                ...groups.map((g) => ({ value: g.id, label: g.name })),
              ]}
            />
          </div>
        )}
        {bindingKind === 'host' && (
          <HostPicker
            org={org}
            value={hostId}
            currentName={check && check.host_id === hostId ? check.host_name : undefined}
            onChange={setHostId}
          />
        )}
        <div className="flex items-center gap-2 text-xs">
          <Switch id="check-enabled" checked={enabled} onCheckedChange={setEnabled} />
          <label htmlFor="check-enabled">{t.checks.enabledField}</label>
        </div>
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
            {t.common.save}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/**
 * HostPicker chooses the monitored host a host-bound definition applies
 * to. The hosts list is paginated, so — like inventory's
 * ManagedHostPicker — it searches through the API (`q`) rather than
 * filtering one page, and says plainly when more hosts match than are
 * listed instead of pretending the first page is the fleet.
 *
 * It is rendered only while the binding is "host", so choosing any other
 * binding costs no request at all.
 *
 * Whatever that search answers, the host this definition is ALREADY bound
 * to stays in the list as its own option: one page of results cannot be
 * assumed to contain it (a fleet paginated past it, a host no longer
 * monitored, a search the operator has since narrowed), and a select with
 * no option matching its value silently falls back to the empty
 * placeholder — the dialog would then state the binding was unset and
 * refuse to submit, with no message anywhere (review finding 1). For the
 * same reason typing in the search box no longer clears the selection.
 */
function HostPicker({
  org, value, currentName, onChange,
}: {
  org: string;
  value: string;
  /** The bound host's name as the definition itself reports it
   * (host_name), when this is an edit of an existing host binding. */
  currentName?: string;
  onChange: (id: string) => void;
}) {
  const t = useT();
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { monitored: true, q });
  const listed = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const currentMissing = value !== '' && !listed.some((h) => h.id === value);

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="check-host">{t.checks.host}</Label>
      <input
        type="search"
        aria-label={t.checks.hostPicker.searchLabel}
        placeholder={t.checks.hostPicker.searchPlaceholder}
        className={SELECT}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <Select
        id="check-host"
        placeholder={t.checks.hostPicker.choose}
        required
        value={value}
        onChange={onChange}
        options={[
          ...(currentMissing ? [{ value, label: t.checks.currentBinding(currentName ?? value) }] : []),
          ...listed.map((h) => ({ value: h.id, label: h.name })),
        ]}
      />
      {hosts.hasNextPage && <span className="text-[11px] text-muted-foreground">{t.checks.hostPicker.moreMatch}</span>}
      {!hosts.data && hosts.isError && (
        <span role="alert" className="text-[11px] text-destructive">
          {t.checks.hostPicker.loadFailed} {hosts.error instanceof Error ? hosts.error.message : ''}
        </span>
      )}
    </div>
  );
}
