import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useCreateCheck, useUpdateCheck } from '../../api/hooks/useChecks.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import type { CheckBindingKind, CheckDefinition, CheckDefinitionInput, CheckPlugin, MonitoringGroup } from '../../api/types.ts';

// Still used by HostPicker's own search <input> below, which stays a plain
// text field rather than becoming a Select.
const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground disabled:opacity-50';
const TEXTAREA = 'min-h-20 w-full rounded-md border border-border bg-background p-2 font-mono text-xs';

const PLUGINS: [CheckPlugin, string][] = [
  ['cpu', 'CPU'],
  ['memory', 'Memory'],
  ['disk', 'Disk'],
  ['service', 'Service units'],
];

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
 */
export function CheckDialog({ org, check, copy = false, groups, onClose }: CheckDialogProps) {
  const editing = !!check && !copy;
  const seeded = editing && check.origin === 'seed';
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
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  // A group the list does not hold: either the list failed to load or the
  // binding names a group this page has not been told about. Either way the
  // definition's own binding stays selectable and savable.
  const currentGroupMissing = groupId !== '' && !groups.some((g) => g.id === groupId);
  const title = editing ? `Edit ${check.key}` : copy && check ? `Copy ${check.key}` : 'New check';
  return (
    <Dialog open onClose={onClose} title={title} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-plugin">Plugin</Label>
            <Select
              id="check-plugin"
              options={PLUGINS.map(([value, label]) => ({ value, label }))}
              value={plugin}
              disabled={seeded}
              onChange={(v) => setPlugin(v as CheckPlugin)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-key">Key</Label>
            <Input
              id="check-key"
              className="font-mono"
              value={key}
              disabled={seeded}
              required={editing}
              // keyPattern, internal/checks/definitions.go — so the
              // refusal arrives before the round trip, and so whitespace
              // (which satisfies `required` on its own) is caught here
              // too. The server rule is still the guard, not this.
              pattern="[a-z0-9][a-z0-9-]{0,62}"
              title="Lower-case letters, digits and hyphens; up to 63 characters, not starting with a hyphen"
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
          <p className="text-[11px] text-muted-foreground">
            This definition ships with every organization; its key and plugin are fixed. Copy it to change either — its interval, parameters,
            binding and enabled state are editable here.
          </p>
        )}
        {copy && (
          <p className="text-[11px] text-muted-foreground">
            A copy needs either a key of its own or a narrower binding than the row it came from: a narrower scope with the same key is what
            overrides that check for those hosts.
          </p>
        )}
        {plugin === 'service' && (
          <>
            <Label htmlFor="check-units">Units (one per line)</Label>
            <textarea
              id="check-units"
              className={TEXTAREA}
              value={units}
              required
              placeholder="sshd"
              onChange={(e) => setUnits(e.target.value)}
            />
          </>
        )}
        {plugin === 'disk' && (
          <>
            <Label htmlFor="check-mounts">Mounts (one per line, empty means every mount)</Label>
            <textarea id="check-mounts" className={TEXTAREA} value={mounts} placeholder="/&#10;/var" onChange={(e) => setMounts(e.target.value)} />
          </>
        )}
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-binding">Binds to</Label>
            <Select
              id="check-binding"
              options={[
                { value: 'org', label: 'The whole organization' },
                { value: 'group', label: `A monitoring group${groups.length === 0 ? ' (none yet)' : ''}`, disabled: groups.length === 0 },
                { value: 'host', label: 'One host' },
              ]}
              value={bindingKind}
              onChange={(v) => setBindingKind(v as CheckBindingKind)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-interval">Interval (seconds)</Label>
            <Input
              id="check-interval"
              type="number"
              min={10}
              max={86400}
              required
              value={interval}
              onChange={(e) => setInterval(e.target.value)}
            />
          </div>
        </div>
        {bindingKind === 'group' && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="check-group">Monitoring group</Label>
            <Select
              id="check-group"
              placeholder="Choose a monitoring group…"
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
                ...(currentGroupMissing ? [{ value: groupId, label: `group ${groupId} (current binding)` }] : []),
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
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Enabled
        </label>
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
  const [q, setQ] = useState('');
  const hosts = useHosts(org, { monitored: true, q });
  const listed = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const currentMissing = value !== '' && !listed.some((h) => h.id === value);

  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="check-host">Host</Label>
      <input
        type="search"
        aria-label="Search monitored hosts"
        placeholder="Search monitored hosts…"
        className={SELECT}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <Select
        id="check-host"
        placeholder="Choose a monitored host…"
        value={value}
        onChange={onChange}
        options={[
          ...(currentMissing ? [{ value, label: `${currentName ?? value} (current binding)` }] : []),
          ...listed.map((h) => ({ value: h.id, label: h.name })),
        ]}
      />
      {hosts.hasNextPage && <span className="text-[11px] text-muted-foreground">More hosts match than are listed — narrow the search.</span>}
      {!hosts.data && hosts.isError && (
        <span role="alert" className="text-[11px] text-destructive">
          Could not load monitored hosts. {hosts.error instanceof Error ? hosts.error.message : ''}
        </span>
      )}
    </div>
  );
}
