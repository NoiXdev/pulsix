import { useEffect, useState, type FormEvent } from 'react';
import { useParams } from '@tanstack/react-router';
import { Layers } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Tag } from '../../ui/badge.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import {
  useCreateMonitoringGroup,
  useDeleteMonitoringGroup,
  useMonitoringGroupPreview,
  useMonitoringGroups,
  useUpdateMonitoringGroup,
} from '../../api/hooks/useChecks.ts';
import { may } from '../../lib/roles.ts';
import type { MonitoringGroup } from '../../api/types.ts';
import { ChecksTabs } from './ChecksTabs.tsx';

const COLUMNS = 'minmax(0, 0.9fr) minmax(0, 1.4fr) 90px 190px';

/** How long the selector field stays quiet before the preview asks. The
 * route expands the expression across the organization's whole host
 * table, so a request per keystroke would be a self-inflicted load
 * problem; this is the same 300 ms the schedule editor's preview uses. */
const PREVIEW_DEBOUNCE_MS = 300;

/**
 * MonitoringGroupsPage is the saved host-tag selectors a check definition
 * can bind to (v0.3b-1 spec, GUI): name, selector and priority, with a
 * live match count while typing — "a selector with no visible matches is
 * guesswork". Same permissions as the definitions: a viewer reads, an
 * operator writes.
 */
export function MonitoringGroupsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'checks.read');
  const canWrite = may(role, 'checks.write');
  const groups = useMonitoringGroups(org, !!orgInfo.data && canRead);
  const remove = useDeleteMonitoringGroup(org);
  const [editing, setEditing] = useState<MonitoringGroup | 'new' | null>(null);

  // Built before any early return, and carried by every one of them: a
  // failed list used to replace the whole page, taking ChecksTabs with it
  // and leaving no link back to the definitions but the sidebar (review
  // finding 5).
  const header = (
    <div className="flex flex-col gap-2">
      <h1 className="text-xl font-semibold">Checks</h1>
      <ChecksTabs org={org} />
    </div>
  );
  const orgGuard = useQueryGuard(orgInfo);
  const groupsGuard = useQueryGuard(groups);
  const guard = orgGuard ?? groupsGuard;
  if (guard) {
    return (
      <>
        {header}
        {guard}
      </>
    );
  }
  if (!orgInfo.data) {
    return (
      <>
        {header}
        <QueryLoading />
      </>
    );
  }
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">Monitoring groups are visible to members of this organization.</p>
      </>
    );
  }
  if (!groups.data) {
    return (
      <>
        {header}
        <QueryLoading />
      </>
    );
  }
  const list = groups.data.groups ?? [];

  async function onDelete(g: MonitoringGroup) {
    if (!confirm(`Delete monitoring group ${g.name}? The check definitions bound to it are deleted with it.`)) return;
    try {
      await remove.mutateAsync(g.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">
          {list.length} groups · a higher priority wins when two groups bind the same check key to one host
        </span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Layers size={15} /> New group
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Group</span>
          <span>Selector</span>
          <span>Priority</span>
          <span />
        </GridHeader>
        {list.map((g) => (
          <GridRow key={g.id} columns={COLUMNS}>
            <span className="truncate text-[13px] font-medium">{g.name}</span>
            <span className="min-w-0">
              <Tag className="truncate">{g.selector}</Tag>
            </span>
            <span className="text-[13px]">{g.priority}</span>
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={`Edit ${g.name}`} onClick={() => setEditing(g)}>
                    Edit
                  </Button>
                  <Button type="button" variant="destructive" aria-label={`Delete ${g.name}`} onClick={() => void onDelete(g)}>
                    Delete
                  </Button>
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No monitoring groups yet.</p>}
      </Card>
      {editing && <GroupDialog org={org} group={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
    </>
  );
}

/** GroupDialog creates or edits one monitoring group, with the live match
 * count the spec asks for beside the selector field. */
function GroupDialog({ org, group, onClose }: { org: string; group?: MonitoringGroup; onClose: () => void }) {
  const create = useCreateMonitoringGroup(org);
  const update = useUpdateMonitoringGroup(org, group?.id ?? '');
  const [name, setName] = useState(group?.name ?? '');
  const [selector, setSelector] = useState(group?.selector ?? '');
  const [priority, setPriority] = useState(String(group?.priority ?? 0));
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const body = { name: name.trim(), selector: selector.trim(), priority: Number(priority) };
      if (group) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  return (
    <Dialog open onClose={onClose} title={group ? `Edit ${group.name}` : 'New monitoring group'} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <div className="grid grid-cols-[minmax(0,1fr)_110px] gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="mg-name">Name</Label>
            <Input id="mg-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="mg-priority">Priority</Label>
            <Input id="mg-priority" type="number" value={priority} onChange={(e) => setPriority(e.target.value)} required />
          </div>
        </div>
        <Label htmlFor="mg-selector">Selector</Label>
        <Input
          id="mg-selector"
          className="font-mono"
          value={selector}
          onChange={(e) => setSelector(e.target.value)}
          required
          placeholder="role=web,env!=dev"
        />
        <SelectorPreview org={org} selector={selector} />
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
 * SelectorPreview resolves what the typed selector currently matches.
 *
 * Two things it deliberately does not do. It does not ask on every
 * keystroke: the field settles for PREVIEW_DEBOUNCE_MS first, and an empty
 * selector is never sent at all (there is no answer worth a round trip,
 * and the server would refuse it anyway). And it does not judge the
 * expression itself — an unparseable selector is reported with the
 * server's own message, which names where the expression went wrong;
 * a second, client-side parser here could only drift from the one that
 * decides.
 *
 * Zero matches is the case this exists for, so it reads as its own
 * sentence in the warning colour rather than as the number 0 in the same
 * type as any other count.
 */
function SelectorPreview({ org, selector }: { org: string; selector: string }) {
  const trimmed = selector.trim();
  const [debounced, setDebounced] = useState<string | null>(trimmed === '' ? null : trimmed);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(trimmed === '' ? null : trimmed), PREVIEW_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [trimmed]);
  const preview = useMonitoringGroupPreview(org, debounced);

  if (debounced === null) {
    return <span className="text-[11px] text-muted-foreground">Type a selector to see which hosts it matches.</span>;
  }
  if (preview.isError) {
    return (
      <p role="alert" className="text-xs text-[var(--px-crit)]">
        {preview.error instanceof Error ? preview.error.message : 'Could not resolve this selector'}
      </p>
    );
  }
  if (!preview.data) {
    return <span className="text-[11px] text-muted-foreground">Matching…</span>;
  }
  const hosts = preview.data.hosts ?? [];
  if (preview.data.count === 0) {
    return (
      <span data-no-match="true" className="text-xs font-semibold text-[var(--px-warn)]">
        No host matches this selector
      </span>
    );
  }
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-xs font-semibold text-[var(--px-ok)]">
        Matches {preview.data.count} host{preview.data.count === 1 ? '' : 's'}
      </span>
      <div className="flex max-h-24 flex-wrap gap-1 overflow-auto">
        {hosts.map((h) => (
          <Tag key={h.id}>{h.monitored ? h.name : `${h.name} (not monitored)`}</Tag>
        ))}
      </div>
    </div>
  );
}
