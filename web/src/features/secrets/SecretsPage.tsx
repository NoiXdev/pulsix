import { useParams } from '@tanstack/react-router';
import { KeyRound } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useSecrets } from '../../api/hooks/useSecrets.ts';
import { may } from '../../lib/roles.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useListView } from '../lists/useListView.ts';
import { secretColumns } from './secretColumns.tsx';
import { useSecretListActions } from './useSecretListActions.tsx';

/**
 * SecretsPage is Settings → Secrets (backlog 3.13d): the organization's
 * secret store, the credentials the checks the Pulsix server runs need
 * (an SNMP community or login). Every member sees the names (checks.read);
 * creating, replacing and deleting need credentials.write, the bar of the
 * other stored credentials. A value is never shown: the API returns none.
 * List id `secrets`: one request, sorted and paged in the browser.
 */
export function SecretsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const s = t.secrets;
  const orgInfo = useOrg(org);
  const canWrite = may(orgInfo.data?.org.role, 'credentials.write');
  const secrets = useSecrets(org);
  const list = secrets.data?.secrets ?? [];
  const actions = useSecretListActions(org, canWrite, list.map((x) => x.name));
  const columns = secretColumns(t, fmt);
  const view = useListView('secrets', columns, { mode: 'client', rowKey: (x) => x.name });

  const guard = useQueryGuard(secrets);
  if (guard) return guard;
  if (!secrets.data) return <QueryLoading />;
  const { rows, total } = view.view(view.ready ? list : undefined);

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{s.title}</h1>
        {canWrite && (
          <Button variant="primary" onClick={actions.openNew}>
            <KeyRound size={15} /> {s.add}
          </Button>
        )}
      </div>
      <span className="text-[13px] text-muted-foreground">{s.subtitle}</span>
      {orgInfo.data && !canWrite && <p className="text-xs text-muted-foreground">{s.readOnly}</p>}
      <DataList
        label={s.label}
        columns={columns}
        rows={rows}
        rowKey={(x) => x.name}
        rowLabel={(x) => x.name}
        rowActions={actions.rowActions}
        loading={!view.ready}
        empty={s.empty}
        {...view.listProps(total, s.count)}
      />
      {actions.dialogs}
    </>
  );
}
