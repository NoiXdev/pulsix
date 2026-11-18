import { useState, type FormEvent } from 'react';
import { useParams } from '@tanstack/react-router';
import { Globe, Server } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { FilterChip, ListToolbar, ResetFilters, SearchInput } from '../../ui/list-toolbar.tsx';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useHostsPage, useZones, type HostPageFilter } from '../../api/hooks/useHosts.ts';
import { useHostQuery } from '../../api/hooks/useTags.ts';
import { useAgents } from '../../api/hooks/useAgents.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import type { HostListItem } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { hostColumns } from './hostColumns.tsx';
import { HostDialog } from './HostDialog.tsx';
import { HOSTS_FILTERS } from './hostsSearch.ts';
import { hostPresence, type AgentPresence } from './presence.ts';
import { useHostListActions } from './useHostListActions.tsx';
import { ZoneDialog } from './ZoneDialog.tsx';

/**
 * HostsListPage is the org's hosts (list id `hosts`). The server sorts and
 * pages the list; its filters (search, zone, the capability chips, "With
 * active runs") are in the URL like sort and page, and start again at page 1. A tag selector instead shows the
 * one answer of POST /hosts/query, sorted and paged in the browser with
 * the same columns.
 */
export function HostsListPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  // The filters live in the URL beside sort and page (hostsSearch.ts), so
  // Back and a reload bring the same list back; a changed filter starts
  // again at page 1. ?q= also comes from the shell's ⌘K search box
  // navigating here.
  const { filters, setFilter, reset } = useListFilters(HOSTS_FILTERS);
  const filter: HostPageFilter = {
    q: filters.q ?? '',
    zone: filters.zone,
    managed: filters.managed,
    monitored: filters.monitored,
    activeRuns: filters.activeRuns,
  };
  const [addOpen, setAddOpen] = useState(false);
  // The page's own "Add zone" button. The host dialog's Zone picker has its
  // own ZoneDialog, since it is what selects the zone it creates.
  const [zoneDialogOpen, setZoneDialogOpen] = useState(false);
  const [selectorText, setSelectorText] = useState('');
  // The selector whose answer the list shows; null shows the paged list.
  const [selector, setSelector] = useState<string | null>(null);

  const zones = useZones(org);
  const agents = useAgents(org);
  const role = useOrg(org).data?.org.role;
  const hostQuery = useHostQuery(org);
  const actions = useHostListActions(org, role);

  async function runSelector(e: FormEvent) {
    e.preventDefault();
    const text = selectorText.trim();
    if (!text) {
      setSelector(null);
      return;
    }
    try {
      await hostQuery.mutateAsync(text);
      setSelector(text);
    } catch {
      setSelector(null);
    }
  }

  // One query for the whole page rather than one per row: the list shows
  // each host's agent as its status dot, and the agents read is org-wide
  // anyway (v0.3a, spec §8).
  const agentByHost = new Map((agents.data?.agents ?? []).map((a) => [a.host_id, a]));
  const zoneOptions = (zones.data?.zones ?? []).map((z) => ({ value: z.id, label: z.name }));
  // A zone only the URL names (a stale link) stays pickable so it can be cleared.
  if (filter.zone && !zoneOptions.some((o) => o.value === filter.zone)) zoneOptions.push({ value: filter.zone, label: filter.zone.slice(0, 8) });
  const columns = hostColumns({ org, t, zones: new Map((zones.data?.zones ?? []).map((z) => [z.id, z])), agents: agentByHost });
  const selectorMode = selector !== null;
  const view = useListView('hosts', columns, {
    mode: selectorMode ? 'client' : 'server',
    resetKey: JSON.stringify({ filter, selector }),
    rowKey: (h) => h.id,
  });
  const hosts = useHostsPage(org, filter, view.serverQuery, { enabled: view.ready && !selectorMode });

  const hostsGuard = useQueryGuard(hosts, { enabled: !selectorMode });
  if (hostsGuard) return hostsGuard;

  let rows: HostListItem[];
  let total: number | undefined;
  let loading: boolean;
  if (selectorMode) {
    ({ rows, total } = view.view(view.ready ? (hostQuery.data?.hosts ?? []) : undefined));
    loading = !view.ready;
  } else {
    rows = hosts.data?.hosts ?? [];
    total = hosts.isPlaceholderData ? undefined : hosts.data?.total;
    loading = hosts.isPending || hosts.isPlaceholderData;
  }
  const filtered = selectorMode || !!filter.q || !!filter.zone || !!filter.managed || !!filter.monitored || !!filter.activeRuns;
  const toggle = (key: 'managed' | 'monitored' | 'activeRuns') => setFilter(key, filter[key] === true ? undefined : true);
  const clearSelector = () => {
    setSelector(null);
    setSelectorText('');
  };

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-0.5">
          <h1 className="text-xl font-semibold">{t.hosts.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">{t.hosts.list.subtitle}</span>
        </div>
        <div className="flex gap-2">
          <Button onClick={() => setZoneDialogOpen(true)}>
            <Globe size={15} /> {t.hosts.list.addZone}
          </Button>
          <Button variant="primary" onClick={() => setAddOpen(true)}>
            <Server size={15} /> {t.hosts.list.addHost}
          </Button>
        </div>
      </div>

      <DataList
        label={t.hosts.list.label}
        columns={columns}
        rows={rows}
        rowKey={(h) => h.id}
        rowLink={(h) => ({ to: '/o/$org/hosts/$id', params: { org, id: h.id }, label: hostLinkLabel(h, agentByHost.get(h.id), t) })}
        rowLabel={(h) => h.name}
        rowActions={actions.rowActions}
        loading={loading}
        empty={filtered ? t.hosts.list.noMatch : t.hosts.list.empty}
        toolbar={
          <ListToolbar>
            <SearchInput
              label={t.hosts.list.search}
              placeholder={t.hosts.list.filterPlaceholder}
              value={filter.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            {/* Backlog 1.12: the zone filter lives here, on the page whose
                endpoint supports it, rather than in the shell as a global
                control that would do nothing on most pages and fight the
                fleet page's own. A plain filter never offers to create —
                narrowing a view is not building one (v0.4g Move 3). */}
            <FilterSelect
              label={t.hosts.list.filterByZone}
              allLabel={t.hosts.list.allZones}
              options={zoneOptions}
              value={filter.zone}
              onChange={(v) => setFilter('zone', v)}
            />
            <FilterChip pressed={filter.managed === true} onToggle={() => toggle('managed')}>
              {t.hosts.capability.managed}
            </FilterChip>
            <FilterChip pressed={filter.monitored === true} onToggle={() => toggle('monitored')}>
              {t.hosts.capability.monitored}
            </FilterChip>
            <FilterChip pressed={filter.activeRuns === true} onToggle={() => toggle('activeRuns')}>
              {t.hosts.list.withActiveRuns}
            </FilterChip>
            <form onSubmit={runSelector} className="flex items-center gap-2">
              <input
                placeholder={t.hosts.list.selectorPlaceholder}
                value={selectorText}
                onChange={(e) => setSelectorText(e.target.value)}
                className="h-[30px] w-56 rounded-md border border-border bg-card px-2.5 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground"
              />
              <Button type="submit">{t.hosts.list.run}</Button>
              {selectorMode && (
                <button type="button" className="text-[11px] text-brand-dim" onClick={clearSelector}>
                  {t.hosts.list.clear}
                </button>
              )}
            </form>
            {hostQuery.error && <span className="text-[11px] text-destructive">{hostQuery.error.message}</span>}
            {filtered && (
              <ResetFilters
                onReset={() => {
                  clearSelector();
                  reset();
                }}
              />
            )}
          </ListToolbar>
        }
        {...view.listProps(total, t.hosts.list.count)}
      />
      {actions.dialogs}

      {zoneDialogOpen && <ZoneDialog org={org} open onClose={() => setZoneDialogOpen(false)} onCreated={() => {}} />}

      <HostDialog org={org} role={role} open={addOpen} onOpenChange={setAddOpen} />
    </>
  );
}

/** A row link's label is all a screen reader announces of the name cell,
 * so it carries the client's presence badge ("Off", "Client") too. */
function hostLinkLabel(host: HostListItem, agent: AgentPresence | undefined, t: Messages): string {
  const presence = hostPresence(host.kind, agent);
  if (presence.kind === 'off') return t.hosts.list.linkLabel(host.name, t.hosts.presence.off);
  if (presence.kind === 'client') return t.hosts.list.linkLabel(host.name, t.hosts.presence.client);
  return host.name;
}
