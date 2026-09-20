import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { User } from 'lucide-react';
import { useOrgActivity } from '../../api/hooks/useActivity.ts';
import { useMembers } from '../../api/hooks/useMembers.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { SettingsTabs } from '../../ui/SettingsTabs.tsx';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';

const OBJECT_TYPES = [
  'host', 'member', 'token', 'zone', 'organization', 'setup', 'totp', 'passkey', 'invite',
  // Automation (spec 2026-09-18 §8): a scheduled run is recorded against
  // its job template.
  'job_template', 'job', 'schedule', 'trigger', 'worker', 'channel', 'maintenance',
];

const ACTOR_KIND_LABEL: Record<string, string> = { token: 'API token', system: 'Pulsix', schedule: 'Schedule', webhook: 'Webhook' };

/**
 * ActivityPage follows design/mockups/build.mjs's ACTIVITY FEED artboard's
 * grouped-by-day list and object-type filter chips. The mockup's right-
 * hand "Event detail" and "Actors" panels depend on data (a before/after
 * diff, per-actor totals) the audit API does not expose — see
 * task-15a-report.md — so this page keeps the feed itself and the filter
 * bar, which the API does support in full (actor, object_type, since).
 *
 * Two fixes from that report's fix round 1: the feed used to show
 * `actor_kind · <uuid prefix>` for every event, and there was no actor
 * filter even though the API supports one (finding "Important 6"). Actor
 * names are now resolved against the members list this page already
 * loads — matching this org's own shape, since the API's `actor` filter
 * is a member's user id, not free text.
 */
export function ActivityPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const [objectType, setObjectType] = useState<string>('');
  const [actorId, setActorId] = useState<string>('');

  const activity = useOrgActivity(org, { objectType: objectType || undefined, actor: actorId || undefined });
  const members = useMembers(org);

  const memberNames = new Map((members.data?.members ?? []).map((m) => [m.user_id, m.display_name]));

  function actorLabel(actorKind: string, actorId?: string | null): string {
    if (actorKind === 'user' && actorId) return memberNames.get(actorId) ?? `user · ${actorId.slice(0, 8)}`;
    return ACTOR_KIND_LABEL[actorKind] ?? actorKind;
  }

  const activityGuard = useQueryGuard(activity);
  if (activityGuard) return activityGuard;

  // The feed is paginated (useOrgActivity follows next_cursor page by
  // page); grouping runs over every loaded page, so a day split across a
  // page boundary still renders as one group.
  const grouped = groupByDay(activity.data?.pages.flatMap((page) => page.items ?? []) ?? []);

  return (
    <>
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold">Activity</h1>
        <SettingsTabs org={org} />
      </div>
      <span className="text-[13px] text-muted-foreground">
        Every change in this organization, by people, tokens and Pulsix itself
      </span>

      <div className="flex flex-wrap items-center gap-2">
        <div className="flex h-[30px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-xs text-muted-foreground">
          <User size={13} />
          <select
            aria-label="Filter by actor"
            value={actorId}
            onChange={(e) => setActorId(e.target.value)}
            className="bg-transparent text-foreground outline-none"
          >
            <option value="">Actor: anyone</option>
            {(members.data?.members ?? []).map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.display_name}
              </option>
            ))}
          </select>
        </div>
        <Chip label="all" on={objectType === ''} onClick={() => setObjectType('')} />
        {OBJECT_TYPES.map((t) => (
          <Chip key={t} label={t} on={objectType === t} onClick={() => setObjectType(t)} />
        ))}
      </div>

      <div className="flex flex-col overflow-hidden rounded-[10px] border border-border bg-card">
        {grouped.length === 0 && (
          <div className="p-4 text-xs text-muted-foreground">
            {activity.isLoading ? 'Loading…' : 'No activity recorded yet.'}
          </div>
        )}
        {grouped.map(([day, items]) => (
          <div key={day} className="flex flex-col">
            <div className="border-b border-border bg-background px-4 py-2 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              {day}
            </div>
            {items.map((e) => (
              <div key={e.id} className="grid grid-cols-[70px_minmax(0,1fr)_110px] items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0">
                <span className="font-mono text-[11px] text-muted-foreground">
                  {new Date(e.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                </span>
                <div className="flex min-w-0 flex-col gap-0.5">
                  <span className="truncate text-[13px]">{e.summary}</span>
                  <span className="text-[11px] text-muted-foreground">{actorLabel(e.actor_kind, e.actor_id)}</span>
                </div>
                <span className="text-right text-[11px] text-muted-foreground">{e.object_type}</span>
              </div>
            ))}
          </div>
        ))}
      </div>
      {activity.hasNextPage && (
        <LoadMoreButton onClick={() => void activity.fetchNextPage()} loading={activity.isFetchingNextPage} />
      )}
    </>
  );
}

function Chip({ label, on, onClick }: { label: string; on: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        on
          ? 'flex h-[30px] items-center rounded-md border border-brand/40 bg-brand/10 px-3 text-xs font-medium text-brand'
          : 'flex h-[30px] items-center rounded-md border border-border bg-card px-3 text-xs font-medium text-muted-foreground'
      }
    >
      {label}
    </button>
  );
}

function groupByDay<T extends { at: string }>(items: T[]): Array<[string, T[]]> {
  const map = new Map<string, T[]>();
  for (const item of items) {
    const day = new Date(item.at).toLocaleDateString(undefined, { weekday: 'long', month: 'short', day: 'numeric' });
    const bucket = map.get(day) ?? [];
    bucket.push(item);
    map.set(day, bucket);
  }
  return [...map.entries()];
}
