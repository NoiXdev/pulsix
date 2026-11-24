import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Radio } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { FilterChip, ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useAgentTokens, useAgents } from '../../api/hooks/useAgents.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { AddAgentDialog } from './AddAgentDialog.tsx';
import { agentColumns } from './agentColumns.tsx';
import { AGENTS_FILTERS, filterAgents } from './agentsSearch.ts';
import { agentTokenColumns, agentTokenName } from './agentTokenColumns.tsx';
import { useAgentListActions } from './useAgentListActions.ts';

/** AgentsPage lists the organization's host agents (v0.3a, spec §8):
 * which host each one runs on, whether it is still reporting (online =
 * a heartbeat within three of the agent's own heartbeat intervals, at
 * least 90 s), its version and its certificate expiry — plus the
 * enrollment tokens waiting to be redeemed.
 * Viewers read; operators enrol and revoke. Both are lists on the shared list
 * component, fetched whole and sorted and paged in the browser: the agents
 * (list id `agents`) keep their sort and page in the URL, the pending tokens
 * (`agent-tokens`) in component state. */
export function AgentsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'agents.read');
  const canWrite = may(role, 'agents.write');
  const list = useAgents(org, !!orgInfo.data && canRead);
  const pending = useAgentTokens(org, !!orgInfo.data && canRead);
  const [adding, setAdding] = useState(false);
  const now = new Date();
  const actions = useAgentListActions(org, canWrite);

  const columns = agentColumns({ t, now });
  const { filters, active, setFilter, reset } = useListFilters(AGENTS_FILTERS);
  const view = useListView('agents', columns, { mode: 'client', rowKey: (a) => a.host_id, resetKey: JSON.stringify(filters) });
  // The pending tokens keep their filter in component state, like their sort and page.
  const [expiredTokens, setExpiredTokens] = useState(false);
  const tokenColumns = agentTokenColumns(t, fmt);
  const tokenView = useListView('agent-tokens', tokenColumns, { mode: 'client', urlState: false, rowKey: (tok) => tok.id, resetKey: String(expiredTokens) });

  const orgGuard = useQueryGuard(orgInfo);
  if (orgGuard) return orgGuard;
  if (!orgInfo.data) return <QueryLoading />;
  if (!canRead) {
    return <p className="text-sm text-muted-foreground">{t.agents.noAccess}</p>;
  }
  if (!list.data) return <QueryLoading />;
  const agents = list.data.agents ?? [];
  const tokens = pending.data?.tokens ?? [];
  const shownAgents = filterAgents(agents, filters);
  const shownTokens = expiredTokens ? tokens.filter((tok) => new Date(tok.expires_at).getTime() <= now.getTime()) : tokens;
  const agentRows = view.view(view.ready ? shownAgents : undefined);
  const tokenRows = tokenView.view(tokenView.ready ? shownTokens : undefined);
  const f = t.agents.filters;

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.agents.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.agents.summary(agents.filter((a) => a.status === 'online').length, agents.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Radio size={15} /> {t.agents.enrol}
          </Button>
        )}
      </div>
      <DataList
        label={t.agents.label}
        columns={columns}
        rows={agentRows.rows}
        rowKey={(a) => a.host_id}
        rowLink={(a) => ({ to: '/o/$org/hosts/$id', params: { org, id: a.host_id }, search: { tab: 'connection' }, label: a.host_name })}
        rowLabel={(a) => a.host_name}
        rowActions={actions.agentActions}
        loading={!view.ready}
        empty={active ? t.agents.noMatch : t.agents.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shownAgents.length} total={agents.length} noun={t.agents.count} />}>
            <SearchInput
              label={t.agents.search}
              placeholder={t.agents.filterPlaceholder}
              value={filters.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            <FilterSelect
              label={f.status}
              allLabel={f.anyStatus}
              options={[
                { value: 'online', label: t.agents.status.online },
                { value: 'offline', label: t.agents.status.offline },
              ]}
              value={filters.status}
              onChange={(v) => setFilter('status', v as typeof filters.status)}
            />
            <FilterChip pressed={!!filters.outdated} tone="warn" onToggle={() => setFilter('outdated', filters.outdated ? undefined : true)}>
              {f.outdated}
            </FilterChip>
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(agentRows.total, t.agents.count)}
      />
      {tokens.length > 0 && (
        <DataList
          label={t.agents.tokensLabel}
          columns={tokenColumns}
          rows={tokenRows.rows}
          rowKey={(tok) => tok.id}
          rowLabel={(tok) => t.agents.tokenName(agentTokenName(tok))}
          rowActions={actions.tokenActions}
          loading={!tokenView.ready}
          empty={null}
          toolbar={
            <ListToolbar count={<ResultCount active={expiredTokens} shown={shownTokens.length} total={tokens.length} noun={t.agents.tokensCount} />}>
              <FilterChip pressed={expiredTokens} tone="crit" onToggle={() => setExpiredTokens(!expiredTokens)}>
                {f.expired}
              </FilterChip>
              {expiredTokens && <ResetFilters onReset={() => setExpiredTokens(false)} />}
            </ListToolbar>
          }
          {...tokenView.listProps(tokenRows.total, t.agents.tokensCount)}
        />
      )}
      {adding && <AddAgentDialog org={org} onClose={() => setAdding(false)} />}
    </>
  );
}
