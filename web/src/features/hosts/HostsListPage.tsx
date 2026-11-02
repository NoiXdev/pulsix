import { useMemo, useState, type FormEvent } from 'react';
import { useParams, useSearch } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { Controller, useForm } from 'react-hook-form';
import { Globe, Search, Server } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';
import { useCreateHost, useHosts, useZones, type HostListFilter } from '../../api/hooks/useHosts.ts';
import { useHostQuery } from '../../api/hooks/useTags.ts';
import { useToast } from '../../ui/toastContext.ts';
import { schemas, type HostFormValues } from '../../lib/schemas.ts';
import { useT } from '../../i18n/useT.ts';
import type { Messages } from '../../i18n/types.ts';
import { useAgents } from '../../api/hooks/useAgents.ts';
import { HostRow } from './HostRow.tsx';
import { ZoneDialog } from './ZoneDialog.tsx';

const HOSTS_COLUMNS = '20px 170px 110px minmax(0, 1fr) 210px 210px 70px 24px';

export function HostsListPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  // ?q= comes from the shell's ⌘K search box (ui/shell.tsx) navigating
  // here — finding "Important 6". Synced into local filter state on
  // mount and whenever it changes, since navigating to this same route
  // with a new search value does not remount the component.
  const routeSearch = useSearch({ strict: false }) as { q?: string };
  const toast = useToast();
  const t = useT();

  const [filter, setFilter] = useState<HostListFilter>({ q: routeSearch.q ?? '' });
  // Adjust local state during render rather than in an effect (React's own
  // guidance for "state that should reset when a prop changes" —
  // https://react.dev/learn/you-might-not-need-an-effect#adjusting-some-state-when-a-prop-changes):
  // an effect-then-setState round trip here would render once with the
  // stale filter before catching up a tick later.
  const [syncedQ, setSyncedQ] = useState(routeSearch.q);
  if (routeSearch.q !== syncedQ) {
    setSyncedQ(routeSearch.q);
    setFilter((f) => ({ ...f, q: routeSearch.q ?? '' }));
  }
  const [addOpen, setAddOpen] = useState(false);
  // Two entry points open ZoneDialog: the page's own "Add zone" button, and
  // the Add host form's Zone picker create row — tracked as one state (not
  // two) so there is exactly one mounted ZoneDialog, and onCreated only
  // reaches into the host form when the picker is what asked for it.
  const [zoneDialog, setZoneDialog] = useState<{ from: 'button' } | { from: 'picker'; name: string } | null>(null);
  const [selectorText, setSelectorText] = useState('');
  const [selectorActive, setSelectorActive] = useState(false);

  const hosts = useHosts(org, filter);
  const zones = useZones(org);
  const agents = useAgents(org);
  const createHost = useCreateHost(org);
  const hostQuery = useHostQuery(org);

  const { hostFormSchema } = useMemo(() => schemas(t), [t]);
  const hostForm = useForm<HostFormValues>({
    resolver: zodResolver(hostFormSchema),
    defaultValues: { name: '', zone_id: '', address: '', managed: false, monitored: false },
  });

  async function runSelector(e: FormEvent) {
    e.preventDefault();
    if (!selectorText.trim()) {
      setSelectorActive(false);
      return;
    }
    try {
      await hostQuery.mutateAsync(selectorText.trim());
      setSelectorActive(true);
    } catch {
      setSelectorActive(false);
    }
  }

  // The plain list is paginated (useHosts follows next_cursor page by
  // page), so what is loaded is the concatenation of every fetched page.
  const listedHosts = hosts.data?.pages.flatMap((page) => page.hosts ?? []) ?? [];
  const displayedHosts = selectorActive ? (hostQuery.data?.hosts ?? []) : listedHosts;
  const zoneSlugById = new Map((zones.data?.zones ?? []).map((z) => [z.id, z.slug]));
  // One query for the whole page rather than one per row: the list shows
  // each host's agent as its status dot, and the agents read is org-wide
  // anyway (v0.3a, spec §8).
  const agentStatusByHost = new Map((agents.data?.agents ?? []).map((a) => [a.host_id, a.status]));

  const hostsGuard = useQueryGuard(hosts, { enabled: !selectorActive });
  if (hostsGuard) return hostsGuard;

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-0.5">
          <h1 className="text-xl font-semibold">{t.hosts.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {hosts.data ? hostCountLabel(listedHosts.length, hosts.hasNextPage, t) : t.common.loading}
          </span>
        </div>
        <div className="flex gap-2">
          <Button onClick={() => setZoneDialog({ from: 'button' })}>
            <Globe size={15} /> {t.hosts.list.addZone}
          </Button>
          <Button variant="primary" onClick={() => setAddOpen(true)}>
            <Server size={15} /> {t.hosts.list.addHost}
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <div className="flex h-[30px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-xs text-muted-foreground">
          <Search size={13} />
          <input
            placeholder={t.hosts.list.filterPlaceholder}
            value={filter.q ?? ''}
            onChange={(e) => setFilter((f) => ({ ...f, q: e.target.value }))}
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
          onChange={(value) => setFilter((f) => ({ ...f, zone: value || undefined }))}
          placeholder={t.hosts.list.allZones}
        />
        <FilterChip
          label={t.hosts.capability.managed}
          on={filter.managed === true}
          onClick={() => setFilter((f) => ({ ...f, managed: f.managed === true ? undefined : true }))}
        />
        <FilterChip
          label={t.hosts.capability.monitored}
          on={filter.monitored === true}
          onClick={() => setFilter((f) => ({ ...f, monitored: f.monitored === true ? undefined : true }))}
        />
        <form onSubmit={runSelector} className="flex items-center gap-2">
          <input
            placeholder={t.hosts.list.selectorPlaceholder}
            value={selectorText}
            onChange={(e) => setSelectorText(e.target.value)}
            className="h-[30px] w-56 rounded-md border border-border bg-card px-2.5 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground"
          />
          <Button type="submit">{t.hosts.list.run}</Button>
          {selectorActive && (
            <button
              type="button"
              className="text-[11px] text-brand-dim"
              onClick={() => {
                setSelectorActive(false);
                setSelectorText('');
              }}
            >
              {t.hosts.list.clear}
            </button>
          )}
        </form>
        {hostQuery.error && <span className="text-[11px] text-destructive">{hostQuery.error.message}</span>}
      </div>

      <Card>
        <GridHeader columns={HOSTS_COLUMNS}>
          <span />
          <span>{t.hosts.list.columns.host}</span>
          <span>{t.hosts.list.columns.zone}</span>
          <span>{t.hosts.list.columns.tags}</span>
          <span>{t.hosts.list.columns.managed}</span>
          <span>{t.hosts.list.columns.monitored}</span>
          <span>{t.hosts.list.columns.seen}</span>
          <span />
        </GridHeader>
        {displayedHosts.length === 0 && (
          <GridRow columns="1fr">
            <span className="text-xs text-muted-foreground">
              {hosts.isLoading ? t.hosts.list.loadingHosts : t.hosts.list.empty}
            </span>
          </GridRow>
        )}
        {displayedHosts.map((h) => (
          <HostRow key={h.id} org={org} host={h} columns={HOSTS_COLUMNS} zoneName={zoneSlugById.get(h.zone_id)} agentStatus={agentStatusByHost.get(h.id)} />
        ))}
      </Card>
      {!selectorActive && hosts.hasNextPage && (
        <LoadMoreButton onClick={() => void hosts.fetchNextPage()} loading={hosts.isFetchingNextPage} />
      )}

      {zoneDialog && (
        <ZoneDialog
          org={org}
          open
          onClose={() => setZoneDialog(null)}
          initialName={zoneDialog.from === 'picker' ? zoneDialog.name : undefined}
          onCreated={(id) => {
            // Only the picker-originated open wants the new zone selected —
            // the page's own "Add zone" button has nothing of its own to
            // put it into; the page's own list refetches via the mutation's
            // invalidation.
            if (zoneDialog.from === 'picker') hostForm.setValue('zone_id', id, { shouldValidate: true });
          }}
        />
      )}

      <Dialog open={addOpen} onClose={() => setAddOpen(false)} title={t.hosts.list.dialog.title}>
        <form
          className="flex flex-col gap-4"
          onSubmit={hostForm.handleSubmit(async (values) => {
            try {
              await createHost.mutateAsync(values);
              toast.show(t.hosts.list.dialog.created(values.name));
              setAddOpen(false);
              hostForm.reset();
            } catch (err) {
              toast.show(err instanceof Error ? err.message : t.hosts.list.dialog.createFailed, 'destructive');
            }
          })}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="host_name">{t.hosts.list.dialog.name}</Label>
            <Input id="host_name" {...hostForm.register('name')} placeholder={t.hosts.list.dialog.namePlaceholder} />
            {hostForm.formState.errors.name && (
              <span className="text-[11px] text-destructive">{hostForm.formState.errors.name.message}</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="host_zone">{t.hosts.list.dialog.zone}</Label>
            <Controller
              control={hostForm.control}
              name="zone_id"
              render={({ field }) => (
                <Select
                  id="host_zone"
                  options={(zones.data?.zones ?? []).map((z) => ({ value: z.id, label: z.name }))}
                  value={field.value}
                  onChange={field.onChange}
                  placeholder={t.hosts.list.dialog.selectZone}
                  create={{ label: t.hosts.list.dialog.newZone, onSelect: (text) => setZoneDialog({ from: 'picker', name: text }) }}
                />
              )}
            />
            {hostForm.formState.errors.zone_id && (
              <span className="text-[11px] text-destructive">{hostForm.formState.errors.zone_id.message}</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="host_address">{t.hosts.list.dialog.address}</Label>
            <Input id="host_address" {...hostForm.register('address')} placeholder={t.hosts.list.dialog.addressPlaceholder} />
          </div>
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" {...hostForm.register('managed')} /> {t.hosts.capability.managedLong}
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" {...hostForm.register('monitored')} /> {t.hosts.capability.monitored}
          </label>
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={() => setAddOpen(false)}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary" disabled={createHost.isPending}>
              {t.hosts.list.dialog.create}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}

/** hostCountLabel never presents a partial list as the total: while more
 * pages exist it says how many are loaded so far, next to the Load more
 * control; only a fully loaded list states a plain count. */
function hostCountLabel(loaded: number, more: boolean, t: Messages): string {
  if (more) return t.hosts.list.loadedMore(loaded);
  return t.hosts.list.count(loaded);
}

function FilterChip({ label, on, onClick }: { label: string; on: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
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
