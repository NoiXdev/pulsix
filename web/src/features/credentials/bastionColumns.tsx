import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Bastion } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { BastionHostKey } from './BastionHostKey.tsx';
import { bastionKeyState } from './bastionKeyState.ts';

export interface BastionColumnsContext {
  t: Messages;
  /** A jump host's name from its id (the one it is reached through). */
  bastionName: (id: string | null | undefined) => string;
  credentialName: (id: string | null | undefined) => string;
}

/**
 * bastionColumns is every column the jump hosts list offers (list id
 * `jump-hosts`), sorted in the browser. The host key column shows the pin
 * read-only; Edit, Delete, Pin and Unpin are the row menu's
 * (useBastionListActions).
 */
export function bastionColumns({ t, bastionName, credentialName }: BastionColumnsContext): DataListColumn<Bastion>[] {
  const c = t.credentials.jumpHosts.columns;
  return [
    {
      id: 'name',
      label: c.name,
      width: 'minmax(0, 1fr)',
      sortKey: 'name',
      sortValue: (b) => b.name,
      required: true,
      defaultVisible: true,
      render: (b) => <span className="block truncate font-mono">{b.name}</span>,
    },
    {
      id: 'login',
      label: c.login,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'login',
      sortValue: (b) => `${b.username}@${b.address}:${b.port}`,
      defaultVisible: true,
      render: (b) => <span className="block truncate font-mono">{`${b.username}@${b.address}:${b.port}`}</span>,
    },
    {
      id: 'reached',
      label: c.reached,
      width: '130px',
      sortKey: 'reached',
      sortValue: (b) => (b.via_bastion_id ? bastionName(b.via_bastion_id) : null),
      defaultVisible: true,
      render: (b) => (
        <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>
          {b.via_bastion_id ? t.credentials.jumpHosts.via(bastionName(b.via_bastion_id)) : t.credentials.jumpHosts.directly}
        </span>
      ),
    },
    {
      id: 'credential',
      label: c.credential,
      width: '130px',
      sortKey: 'credential',
      sortValue: (b) => credentialName(b.credential_id),
      defaultVisible: true,
      render: (b) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{credentialName(b.credential_id)}</span>,
    },
    {
      id: 'host_key',
      label: c.hostKey,
      width: 'minmax(0, 1.6fr)',
      sortKey: 'host_key',
      sortValue: (b) => bastionKeyState(b) || null,
      defaultVisible: true,
      render: (b) => <BastionHostKey bastion={b} />,
    },
  ];
}
