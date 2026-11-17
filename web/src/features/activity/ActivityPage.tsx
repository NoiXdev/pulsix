import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { useOrgActivityPage } from '../../api/hooks/useActivity.ts';
import { useMembers } from '../../api/hooks/useMembers.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { Select } from '../../ui/Select.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useListView } from '../lists/useListView.ts';
import { activityColumns } from './activityColumns.tsx';
import { ObjectTypeChips } from './ObjectTypeChips.tsx';

/**
 * ActivityPage follows design/mockups/build.mjs's ACTIVITY FEED artboard's
 * object-type filter chips (the feed itself is a paged table, not the
 * mockup's day groups). The mockup's right-
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
 *
 * The feed is a list (id `activity`): the server pages it and sorts it by
 * time; the actor and object-type filters start again at page 1.
 */
export function ActivityPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const [objectType, setObjectType] = useState<string>('');
  const [actorId, setActorId] = useState<string>('');

  const members = useMembers(org);
  const memberNames = new Map((members.data?.members ?? []).map((m) => [m.user_id, m.display_name]));

  const columns = activityColumns({ t, fmt, memberNames });
  const view = useListView('activity', columns, { mode: 'server', resetKey: JSON.stringify({ objectType, actorId }) });
  const activity = useOrgActivityPage(org, { objectType, actor: actorId }, view.serverQuery, { enabled: view.ready });

  const guard = useQueryGuard(activity);
  if (guard) return guard;

  return (
    <>
      <h1 className="text-xl font-semibold">{t.activity.title}</h1>
      <span className="text-[13px] text-muted-foreground">{t.activity.subtitle}</span>

      <DataList
        label={t.activity.list.label}
        columns={columns}
        rows={activity.data?.items ?? []}
        rowKey={(e) => e.id}
        loading={activity.isPending || activity.isPlaceholderData}
        empty={t.activity.empty}
        toolbar={
          <div className="flex flex-wrap items-center gap-2">
            {/* A plain filter never offers to create — narrowing a view is not
                building one (v0.4g Move 3, same reasoning as HostsListPage's
                Filter by zone). */}
            <Select
              aria-label={t.activity.filterByActor}
              options={(members.data?.members ?? []).map((m) => ({ value: m.user_id, label: m.display_name }))}
              value={actorId}
              onChange={setActorId}
              placeholder={t.activity.anyActor}
            />
            <ObjectTypeChips value={objectType} onChange={setObjectType} />
          </div>
        }
        {...view.listProps(activity.isPlaceholderData ? undefined : activity.data?.total, t.activity.list.count)}
      />
    </>
  );
}
