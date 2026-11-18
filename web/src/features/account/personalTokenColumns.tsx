import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { PersonalToken } from '../../api/hooks/usePersonalTokens.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { displayedAreas, type Area } from '../tokens/areas.ts';
import { isExpired } from '../tokens/expiry.ts';
import { GrantLine } from './PersonalTokenCells.tsx';

/**
 * personalTokenColumns is every column of the person's own token list (list
 * id `personal-tokens`), sorted in the browser. A token opens nothing, so
 * the name is plain text; an assistant token's areas stay under it.
 */
export function personalTokenColumns(t: Messages, fmt: Format): DataListColumn<PersonalToken>[] {
  const c = t.personalTokens.columns;
  return [
    {
      id: 'token',
      label: c.name,
      width: 'minmax(0, 1.4fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => (
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="truncate font-semibold">{r.name}</span>
          {r.kind === 'assistant' && (r.areas ?? []).length > 0 && (
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
      id: 'kind',
      label: c.kind,
      width: '130px',
      sortKey: 'kind',
      sortValue: (r) => r.kind,
      defaultVisible: true,
      render: (r) => <span className="truncate">{t.tokens.kind[r.kind]}</span>,
    },
    {
      id: 'orgs',
      label: c.orgs,
      width: 'minmax(0, 1.4fr)',
      sortKey: 'orgs',
      sortValue: (r) => (r.grants ?? []).length,
      defaultVisible: true,
      render: (r) => (
        <span className="flex min-w-0 flex-col gap-0.5">
          {(r.grants ?? []).map((g) => (
            <GrantLine key={g.org_id} grant={g} t={t} />
          ))}
        </span>
      ),
    },
    {
      id: 'last_used',
      label: c.lastUsed,
      width: '130px',
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
    {
      id: 'created',
      label: c.created,
      width: '120px',
      sortKey: 'created',
      sortValue: (r) => r.created_at,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.date(r.created_at)}</span>,
    },
  ];
}
