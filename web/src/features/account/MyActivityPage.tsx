import { useState } from 'react';
import { DataList, type DataListColumn } from '../../ui/data-list/index.ts';
import { QueryError } from '../../ui/QueryState.tsx';
import { useMyActivityPage } from '../../api/hooks/useActivity.ts';
import { useOrgs } from '../../api/hooks/useOrgs.ts';
import type { Event } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { ObjectTypeChips } from '../activity/ObjectTypeChips.tsx';
import { known } from '../activity/objectTypes.ts';
import { useListView } from '../lists/useListView.ts';

/**
 * MyActivityPage is Settings > My activity: what the signed-in person did,
 * in every organization they belong to (GET /me/activity). The server
 * pages and sorts it by time; the object-type chips filter it like the
 * organization feed's.
 */
export function MyActivityPage() {
  const t = useT();
  const fmt = useFormat();
  const orgs = useOrgs();
  const [objectType, setObjectType] = useState('');
  const orgNames = new Map((orgs.data?.orgs ?? []).map((o) => [o.id, o.name]));

  const columns: DataListColumn<Event>[] = [
    {
      id: 'at',
      label: t.activity.my.at,
      width: '170px',
      sortKey: 'at',
      required: true,
      defaultVisible: true,
      render: (e) => <span className="font-mono text-muted-foreground">{fmt.dateTime(e.at, { dateStyle: 'medium', timeStyle: 'short' })}</span>,
    },
    {
      id: 'organization',
      label: t.activity.my.organization,
      width: '160px',
      defaultVisible: true,
      render: (e) => <span className="truncate">{(e.org_id && orgNames.get(e.org_id)) || '—'}</span>,
    },
    {
      id: 'action',
      label: t.activity.my.action,
      width: 'minmax(0,1fr)',
      defaultVisible: true,
      render: (e) => <span className="truncate">{e.summary}</span>,
    },
    {
      id: 'object',
      label: t.activity.my.object,
      width: '130px',
      defaultVisible: true,
      render: (e) => <span className="text-muted-foreground">{known(t.activity.objectTypes, e.object_type)}</span>,
    },
  ];

  const view = useListView('my-activity', columns, { mode: 'server', resetKey: JSON.stringify({ objectType }) });
  const q = useMyActivityPage({ objectType }, view.serverQuery, { enabled: view.ready });

  if (q.isError && !q.data) return <QueryError error={q.error} />;

  return (
    <>
      <div className="flex flex-col gap-1">
        <h1 className="text-xl font-semibold">{t.activity.my.title}</h1>
        <span className="text-[13px] text-muted-foreground">{t.activity.my.subtitle}</span>
      </div>
      <DataList
        label={t.activity.my.label}
        columns={columns}
        rows={q.data?.items ?? []}
        rowKey={(e) => e.id}
        loading={q.isPending || q.isPlaceholderData}
        empty={t.activity.my.empty}
        toolbar={
          <div className="flex flex-wrap items-center gap-2">
            <ObjectTypeChips value={objectType} onChange={setObjectType} />
          </div>
        }
        {...view.listProps(q.isPlaceholderData ? undefined : q.data?.total, t.activity.my.count)}
      />
    </>
  );
}
