import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Credential } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import type { useFormat } from '../../i18n/format.ts';
import { expiryState, kindLabel, usageLabel } from './labels.ts';

export interface CredentialColumnsContext {
  t: Messages;
  fmt: ReturnType<typeof useFormat>;
}

/**
 * credentialColumns is every column the credentials list offers (list id
 * `credentials`), sorted in the browser. A credential has no page of its
 * own: its row opens the details dialog (CredentialsPage's onRowActivate),
 * as does the row menu's Open. The name is the required column, so it is
 * first and always shown, and holds the row's button.
 */
export function credentialColumns({ t, fmt }: CredentialColumnsContext): DataListColumn<Credential>[] {
  const c = t.credentials.list.columns;
  return [
    {
      id: 'name',
      label: c.name,
      width: 'minmax(0, 1fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-mono">{r.name}</span>,
    },
    {
      id: 'kind',
      label: c.kind,
      width: '190px',
      sortKey: 'kind',
      sortValue: (r) => kindLabel(r, t),
      defaultVisible: true,
      render: (r) => <span className="block truncate">{kindLabel(r, t)}</span>,
    },
    {
      id: 'used_by',
      label: c.usedBy,
      width: 'minmax(0, 1fr)',
      sortKey: 'used_by',
      sortValue: (r) => r.used_by.hosts + r.used_by.bastions + r.used_by.repos + r.used_by.templates,
      defaultVisible: true,
      render: (r) => (
        <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>
          {r.username ? `${r.username}@ · ` : ''}
          {usageLabel(r, t)}
        </span>
      ),
    },
    {
      id: 'expiry',
      label: c.expiry,
      width: '130px',
      sortKey: 'expiry',
      sortValue: (r) => r.expires_at ?? null,
      defaultVisible: true,
      render: (r) => {
        const expiry = expiryState(r, t);
        return expiry ? <Pill tone={expiry.tone}>{expiry.text}</Pill> : <span className={`text-muted-foreground ${CELL_MINOR}`}>—</span>;
      },
    },
    {
      id: 'created',
      label: c.created,
      width: '110px',
      sortKey: 'created',
      sortValue: (r) => r.created_at,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.date(r.created_at)}</span>,
    },
  ];
}
