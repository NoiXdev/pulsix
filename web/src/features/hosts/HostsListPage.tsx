import { useState, type FormEvent } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Globe, Search, Server } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { Select } from '../../ui/Select.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useHostsPage, useZones, type HostPageFilter } from '../../api/hooks/useHosts.ts';
import { useHostQuery } from '../../api/hooks/useTags.ts';
import { useAgents } from '../../api/hooks/useAgents.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import type { HostListItem } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useListView } from '../lists/useListView.ts';
import { hostColumns } from './hostColumns.tsx';
import { HostDialog } from './HostDialog.tsx';
import type { HostsSearch } from './hostsSearch.ts';
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
  const navigate = useNavigate();
  // The filters live in the URL beside sort and page (hostsSearch.ts), so
  // Back and a reload bring the same list back. ?q= also comes from the
  // shell's ⌘K search box navigating here.
  const routeSearch = useSearch({ strict: false }) as HostsSearch;
  const filter: HostPageFilter = {
    q: routeSearch.q ?? '',
    zone: routeSearch.zone,
    managed: routeSearch.managed,
    monitored: routeSearch.monitored,
    activeRuns: routeSearch.activeRuns,
  };
  // A changed filter starts again at page 1. Typing replaces the history
  // entry instead of adding one per keystroke.
  function setFilter(patch: Partial<HostsSearch>, replace = false) {
    void navigate({
      to: '.',
      search: (prev: Record<string, unknown>) => ({ ...prev, ...patch, page: undefined }),
      replace,
      resetScroll: false,
    });
  }
  // The search box keeps its own text so typing is not at the mercy of the
  // router round trip; it follows the URL when that changes (Back, ⌘K).
  // Adjusted during render rather than in an effect (React's own guidance,
  // https://react.dev/learn/you-might-not-need-an-effect#adjusting-some-state-when-a-prop-changes).
  const [qText, setQText] = useState(routeSearch.q ?? '');
  const [syncedQ, setSyncedQ] = useState(routeSearch.q);
  if (routeSearch.q !== syncedQ) {
    setSyncedQ(routeSearch.q);
    setQText(routeSearch.q ?? '');
  }
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
  const toggle = (key: 'managed' | 'monitored' | 'activeRuns') => setFilter({ [key]: filter[key] === true ? undefined : true });

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
          <>
            <div className="flex h-[30px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-xs text-muted-foreground">
              <Search size={13} />
              <input
                placeholder={t.hosts.list.filterPlaceholder}
                value={qText}
                onChange={(e) => {
                  setQText(e.target.value);
                  setFilter({ q: e.target.value || undefined }, true);
                }}
                className="w-40 bg-transparent text-foreground outline-none placeholder:text-muted-foreground"
              />
            </div>
            {/* Backlog 1.12: the zone filter lives here, on the page whose
                endpoint supports it, rather than in the shell as a global
                control that would do nothing on most pages and fight the
                fleet page's own. A plain filter never offers to create —
                narrowing a view is not building one (v0.4g Move 3). */}
            <Select
              aria-label={t.hosts.list.filterByZone}
              options={(zones.data?.zones ?? []).map((z) => ({ value: z.id, label: z.name }))}
              value={filter.zone ?? ''}
              onChange={(value) => setFilter({ zone: value || undefined })}
              placeholder={t.hosts.list.allZones}
            />
            <FilterChip label={t.hosts.capability.managed} on={filter.managed === true} onClick={() => toggle('managed')} />
            <FilterChip label={t.hosts.capability.monitored} on={filter.monitored === true} onClick={() => toggle('monitored')} />
            <FilterChip label={t.hosts.list.withActiveRuns} on={filter.activeRuns === true} onClick={() => toggle('activeRuns')} />
            <form onSubmit={runSelector} className="flex items-center gap-2">
              <input
                placeholder={t.hosts.list.selectorPlaceholder}
                value={selectorText}
                onChange={(e) => setSelectorText(e.target.value)}
                className="h-[30px] w-56 rounded-md border border-border bg-card px-2.5 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground"
              />
              <Button type="submit">{t.hosts.list.run}</Button>
              {selectorMode && (
                <button
                  type="button"
                  className="text-[11px] text-brand-dim"
                  onClick={() => {
                    setSelector(null);
                    setSelectorText('');
                  }}
                >
                  {t.hosts.list.clear}
                </button>
              )}
            </form>
            {hostQuery.error && <span className="text-[11px] text-destructive">{hostQuery.error.message}</span>}
          </>
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

function FilterChip({ label, on, onClick }: { label: string; on: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={
        on
          ? 'flex h-[30px] items-center rounded-md border border-brand/40 bg-brand/10 px-3 text-xs font-medium text-brand'
          : 'flex h-[30px] items-center rounded-md border border-border bg-card px-3 text-xs font-medium text-muted-foreground'
      }
    >
      {label}
    </button>
  );
}
