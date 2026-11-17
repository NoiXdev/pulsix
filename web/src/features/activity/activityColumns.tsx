import type { Event } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import { known } from './objectTypes.ts';

export interface ActivityColumnsContext {
  t: Messages;
  fmt: Format;
  /** A member's display name by user id. */
  memberNames: ReadonlyMap<string, string>;
}

/** Who did it: a member by name, a token as the member who made it plus the
 * token's name, anything else by the kind of actor. */
export function actorLabel({ t, memberNames }: ActivityColumnsContext, e: Pick<Event, 'actor_kind' | 'actor_id' | 'token_name'>): string {
  let who: string;
  if (e.actor_kind === 'user' && e.actor_id) who = memberNames.get(e.actor_id) ?? t.activity.user(e.actor_id.slice(0, 8));
  // A token acts for the member who created it, and the API names that
  // member as the actor.
  else if (e.actor_kind === 'token' && e.actor_id && memberNames.has(e.actor_id)) who = memberNames.get(e.actor_id) as string;
  else who = known(t.activity.actors, e.actor_kind);
  return e.token_name ? `${who} · ${t.activity.viaToken(e.token_name)}` : who;
}

/**
 * activityColumns is every column the organization's activity list offers
 * (list id `activity`). The server sorts by time only (newest first by
 * default), so only When is sortable.
 */
export function activityColumns(ctx: ActivityColumnsContext): DataListColumn<Event>[] {
  const { t, fmt } = ctx;
  const c = t.activity.list.columns;
  return [
    {
      id: 'at',
      label: c.at,
      width: '170px',
      sortKey: 'at',
      required: true,
      defaultVisible: true,
      render: (e) => <span className={`font-mono text-muted-foreground ${CELL_MINOR}`}>{fmt.dateTime(e.at, { dateStyle: 'medium', timeStyle: 'short' })}</span>,
    },
    {
      id: 'actor',
      label: c.actor,
      width: 'minmax(0,0.7fr)',
      defaultVisible: true,
      render: (e) => <span className="block truncate">{actorLabel(ctx, e)}</span>,
    },
    {
      id: 'action',
      label: c.action,
      width: 'minmax(0,1.3fr)',
      defaultVisible: true,
      render: (e) => <span className="block truncate">{e.summary}</span>,
    },
    {
      id: 'object',
      label: c.object,
      width: '130px',
      defaultVisible: true,
      render: (e) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{known(t.activity.objectTypes, e.object_type)}</span>,
    },
  ];
}
