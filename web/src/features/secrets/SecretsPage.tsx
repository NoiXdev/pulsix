import { useParams } from '@tanstack/react-router';
import { KeyRound } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { FilterChip, ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useSecrets } from '../../api/hooks/useSecrets.ts';
import { may } from '../../lib/roles.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { SECRETS_FILTERS, filterSecrets } from './secretsSearch.ts';
import { secretColumns } from './secretColumns.tsx';
import { useSecretListActions } from './useSecretListActions.tsx';
import { PageHeader } from '../../ui/settings.tsx';

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
  const { filters, active, setFilter, reset } = useListFilters(SECRETS_FILTERS);
  const view = useListView('secrets', columns, { mode: 'client', rowKey: (x) => x.name, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(secrets);
  if (guard) return guard;
  if (!secrets.data) return <QueryLoading />;
  const shown = filterSecrets(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);

  return (
    <>
      <PageHeader
        title={s.title}
        description={s.subtitle}
        actions={
          canWrite ? (
            <Button variant="primary" onClick={actions.openNew}>
              <KeyRound size={15} /> {s.add}
            </Button>
          ) : undefined
        }
      />
      {orgInfo.data && !canWrite && <p className="text-xs text-muted-foreground">{s.readOnly}</p>}
      <DataList
        label={s.label}
        columns={columns}
        rows={rows}
        rowKey={(x) => x.name}
        rowLabel={(x) => x.name}
        rowActions={actions.rowActions}
        loading={!view.ready}
        empty={active ? s.noMatch : s.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={list.length} noun={s.count} />}>
            <SearchInput label={s.search} placeholder={s.filterPlaceholder} value={filters.q ?? ''} onChange={(q) => setFilter('q', q || undefined, { replace: true })} />
            <FilterChip pressed={!!filters.unused} onToggle={() => setFilter('unused', filters.unused ? undefined : true)}>
              {s.filterUnused}
            </FilterChip>
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, s.count)}
      />
      {actions.dialogs}
    </>
  );
}
