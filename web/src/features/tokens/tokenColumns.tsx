import { Pill, RoleBadge, Tag } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { components } from '../../api/schema.d.ts';
import type { PersonalTokenEntry } from '../../api/hooks/usePersonalTokens.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { roleRank } from '../members/roles.ts';
import { displayedAreas, type Area } from './areas.ts';
import { isExpired } from './expiry.ts';
import { PersonalState } from './TokenCells.tsx';

type Token = components['schemas']['TokenDTO'];

/** One row of the organization's token list: an organization token, or a
 * member's personal token granted here (spec rule 6). */
export type TokenRow = ({ type: 'org' } & Token) | ({ type: 'personal' } & PersonalTokenEntry);

/** A row's key: organization and personal token ids never collide. */
export function tokenRowKey(r: TokenRow): string {
  return r.type === 'org' ? `org:${r.id}` : `personal:${r.token_id}`;
}

/**
 * tokenColumns is every column the organization's API token list offers
 * (list id `org-tokens`), sorted in the browser: its own tokens and the
 * personal tokens members granted here, tagged Personal with their owner. A
 * token opens nothing, so the name is plain text: an assistant token's badge
 * and its areas, and a personal entry's block, stay under the name at every
 * density.
 */
export function tokenColumns(t: Messages, fmt: Format): DataListColumn<TokenRow>[] {
  const c = t.tokens.columns;
  return [
    {
      id: 'token',
      label: c.name,
      width: 'minmax(0, 1.6fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => (
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="flex min-w-0 items-center gap-2">
            <span className="truncate font-semibold">{r.name}</span>
            {r.type === 'personal' && <Tag>{t.tokens.personal.tag}</Tag>}
            {r.kind === 'assistant' && <Tag>{t.tokens.kind.badge}</Tag>}
          </span>
          {r.type === 'personal' && (r.blocked || r.owner_blocked || r.token_revoked) && <PersonalState row={r} t={t} />}
          {r.type === 'org' && r.kind === 'assistant' && (r.areas ?? []).length > 0 && (
            <span className={`truncate text-muted-foreground ${CELL_MINOR}`}>
              {displayedAreas(r.areas ?? [])
                .map((area) => (Object.hasOwn(t.tokens.areas.labels, area) ? t.tokens.areas.labels[area as Area] : area))
                .join(' · ')}
            </span>
          )}
        </span>
      ),
    },
    {
      id: 'owner',
      label: c.owner,
      width: 'minmax(0, 1fr)',
      sortKey: 'owner',
      sortValue: (r) => (r.type === 'personal' ? r.owner_name : undefined),
      defaultVisible: true,
      render: (r) =>
        r.type === 'personal' ? (
          <span className="truncate" title={r.owner_email}>
            {r.owner_name}
          </span>
        ) : (
          <span className={`text-muted-foreground ${CELL_MINOR}`}>—</span>
        ),
    },
    {
      id: 'role',
      label: c.role,
      width: '110px',
      sortKey: 'role',
      sortValue: (r) => roleRank(r.role),
      defaultVisible: true,
      render: (r) => (
        <span>
          <RoleBadge role={r.role} />
        </span>
      ),
    },
    {
      id: 'last_used',
      label: c.lastUsed,
      width: '140px',
      sortKey: 'last_used',
      sortValue: (r) => r.last_used_at,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{r.last_used_at ? fmt.date(r.last_used_at) : t.common.never}</span>,
    },
    {
      id: 'expires',
      label: c.expires,
      width: '140px',
      sortKey: 'expires',
      sortValue: (r) => r.expires_at,
      defaultVisible: true,
      render: (r) => (
        <span className={`flex items-center gap-1.5 text-muted-foreground ${CELL_MINOR}`}>
          {r.expires_at ? fmt.date(r.expires_at) : t.common.never}
          {isExpired(r.expires_at) && (
            <span title={t.tokens.expiredHint}>
              <Pill tone="unknown">{t.tokens.expired}</Pill>
              <span className="sr-only">. {t.tokens.expiredHint}</span>
            </span>
          )}
        </span>
      ),
    },
  ];
}
