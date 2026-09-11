import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { useOrgActivity } from '../../api/hooks/useActivity.ts';

const OBJECT_TYPES = ['host', 'member', 'token', 'zone', 'organization', 'setup', 'totp', 'passkey', 'invite'];

/**
 * ActivityPage follows design/mockups/build.mjs's ACTIVITY FEED artboard's
 * grouped-by-day list and object-type filter chips. The mockup's right-
 * hand "Event detail" and "Actors" panels depend on data (a before/after
 * diff, per-actor totals) the audit API does not expose — see
 * task-15a-report.md — so this page keeps the feed itself and the filter
 * bar, which the API does support in full (actor, object_type, since).
 */
export function ActivityPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const [objectType, setObjectType] = useState<string>('');

  const activity = useOrgActivity(org, { objectType: objectType || undefined });

  const grouped = groupByDay(activity.data?.items ?? []);

  return (
    <>
      <div className="flex flex-col gap-0.5">
        <h1 className="text-xl font-semibold">Activity</h1>
        <span className="text-[13px] text-muted-foreground">
          Every change in this organization, by people, tokens and Pulsix itself
        </span>
      </div>

      <div className="flex flex-wrap items-center gap-2">
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
                  <span className="text-[11px] text-muted-foreground">
                    {e.actor_kind}
                    {e.actor_id ? ` · ${e.actor_id.slice(0, 8)}` : ''}
                  </span>
                </div>
                <span className="text-right text-[11px] text-muted-foreground">{e.object_type}</span>
              </div>
            ))}
          </div>
        ))}
      </div>
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
          ? 'flex h-[30px] items-center rounded-md border border-accent/40 bg-accent/10 px-3 text-xs font-medium text-accent'
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
