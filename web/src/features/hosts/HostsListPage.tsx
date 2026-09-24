import { useState, type FormEvent } from 'react';
import { useParams, useSearch } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useForm } from 'react-hook-form';
import { Globe, Search, Server } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { LoadMoreButton } from '../../ui/LoadMoreButton.tsx';
import { useCreateHost, useCreateZone, useHosts, useZones, type HostListFilter } from '../../api/hooks/useHosts.ts';
import { useHostQuery } from '../../api/hooks/useTags.ts';
import { useToast } from '../../ui/toastContext.ts';
import { hostFormSchema, zoneFormSchema, type HostFormValues, type ZoneFormValues } from '../../lib/schemas.ts';
import { useAgents } from '../../api/hooks/useAgents.ts';
import { HostRow } from './HostRow.tsx';

const HOSTS_COLUMNS = '20px 170px 110px minmax(0, 1fr) 210px 210px 70px 24px';

export function HostsListPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  // ?q= comes from the shell's ⌘K search box (ui/shell.tsx) navigating
  // here — finding "Important 6". Synced into local filter state on
  // mount and whenever it changes, since navigating to this same route
  // with a new search value does not remount the component.
  const routeSearch = useSearch({ strict: false }) as { q?: string };
  const toast = useToast();

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
  const [addZoneOpen, setAddZoneOpen] = useState(false);
  const [selectorText, setSelectorText] = useState('');
  const [selectorActive, setSelectorActive] = useState(false);

  const hosts = useHosts(org, filter);
  const zones = useZones(org);
  const agents = useAgents(org);
  const createHost = useCreateHost(org);
  const createZone = useCreateZone(org);
  const hostQuery = useHostQuery(org);

  const hostForm = useForm<HostFormValues>({
    resolver: zodResolver(hostFormSchema),
    defaultValues: { name: '', zone_id: '', address: '', managed: false, monitored: false },
  });
  const zoneForm = useForm<ZoneFormValues>({ resolver: zodResolver(zoneFormSchema), defaultValues: { slug: '', name: '' } });

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
          <h1 className="text-xl font-semibold">Hosts</h1>
          <span className="text-[13px] text-muted-foreground">
            {hosts.data ? hostCountLabel(listedHosts.length, hosts.hasNextPage) : 'Loading…'}
          </span>
        </div>
        <div className="flex gap-2">
          <Button onClick={() => setAddZoneOpen(true)}>
            <Globe size={15} /> Add zone
          </Button>
          <Button variant="primary" onClick={() => setAddOpen(true)}>
            <Server size={15} /> Add host
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <div className="flex h-[30px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-xs text-muted-foreground">
          <Search size={13} />
          <input
            placeholder="Filter hosts…"
            value={filter.q ?? ''}
            onChange={(e) => setFilter((f) => ({ ...f, q: e.target.value }))}
            className="w-40 bg-transparent text-foreground outline-none placeholder:text-muted-foreground"
          />
        </div>
        <FilterChip
          label="Managed"
          on={filter.managed === true}
          onClick={() => setFilter((f) => ({ ...f, managed: f.managed === true ? undefined : true }))}
        />
        <FilterChip
          label="Monitored"
          on={filter.monitored === true}
          onClick={() => setFilter((f) => ({ ...f, monitored: f.monitored === true ? undefined : true }))}
        />
        <form onSubmit={runSelector} className="flex items-center gap-2">
          <input
            placeholder="selector, e.g. role = db"
            value={selectorText}
            onChange={(e) => setSelectorText(e.target.value)}
            className="h-[30px] w-56 rounded-md border border-border bg-card px-2.5 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground"
          />
          <Button type="submit">Run</Button>
          {selectorActive && (
            <button
              type="button"
              className="text-[11px] text-brand-dim"
              onClick={() => {
                setSelectorActive(false);
                setSelectorText('');
              }}
            >
              clear
            </button>
          )}
        </form>
        {hostQuery.error && <span className="text-[11px] text-destructive">{hostQuery.error.message}</span>}
      </div>

      <Card>
        <GridHeader columns={HOSTS_COLUMNS}>
          <span />
          <span>Host</span>
          <span>Zone</span>
          <span>Tags</span>
          <span>Managed · Ansible</span>
          <span>Monitored</span>
          <span>Seen</span>
          <span />
        </GridHeader>
        {displayedHosts.length === 0 && (
          <GridRow columns="1fr">
            <span className="text-xs text-muted-foreground">
              {hosts.isLoading ? 'Loading hosts…' : 'No hosts yet. Add one to get started.'}
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

      <Dialog open={addZoneOpen} onClose={() => setAddZoneOpen(false)} title="Add zone">
        <form
          className="flex flex-col gap-4"
          onSubmit={zoneForm.handleSubmit(async (values) => {
            try {
              await createZone.mutateAsync(values);
              toast.show(`Zone ${values.name} created`);
              setAddZoneOpen(false);
              zoneForm.reset();
            } catch (err) {
              toast.show(err instanceof Error ? err.message : 'failed to create zone', 'destructive');
            }
          })}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="zone_name">Name</Label>
            <Input id="zone_name" {...zoneForm.register('name')} />
            {zoneForm.formState.errors.name && (
              <span className="text-[11px] text-destructive">{zoneForm.formState.errors.name.message}</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="zone_slug">Slug</Label>
            <Input id="zone_slug" {...zoneForm.register('slug')} />
            {zoneForm.formState.errors.slug && (
              <span className="text-[11px] text-destructive">{zoneForm.formState.errors.slug.message}</span>
            )}
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={() => setAddZoneOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={createZone.isPending}>
              Create zone
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog open={addOpen} onClose={() => setAddOpen(false)} title="Add host">
        <form
          className="flex flex-col gap-4"
          onSubmit={hostForm.handleSubmit(async (values) => {
            try {
              await createHost.mutateAsync(values);
              toast.show(`Host ${values.name} created`);
              setAddOpen(false);
              hostForm.reset();
            } catch (err) {
              toast.show(err instanceof Error ? err.message : 'failed to create host', 'destructive');
            }
          })}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="host_name">Name</Label>
            <Input id="host_name" {...hostForm.register('name')} placeholder="web-04.fra" />
            {hostForm.formState.errors.name && (
              <span className="text-[11px] text-destructive">{hostForm.formState.errors.name.message}</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="host_zone">Zone</Label>
            <select
              id="host_zone"
              {...hostForm.register('zone_id')}
              className="h-8 rounded-md border border-border bg-background px-2.5 text-xs text-foreground"
            >
              <option value="">Select a zone…</option>
              {(zones.data?.zones ?? []).map((z) => (
                <option key={z.id} value={z.id}>
                  {z.name}
                </option>
              ))}
            </select>
            {hostForm.formState.errors.zone_id && (
              <span className="text-[11px] text-destructive">{hostForm.formState.errors.zone_id.message}</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="host_address">Address</Label>
            <Input id="host_address" {...hostForm.register('address')} placeholder="203.0.113.4 or hostname" />
          </div>
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" {...hostForm.register('managed')} /> Managed (Ansible via SSH)
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" {...hostForm.register('monitored')} /> Monitored
          </label>
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={() => setAddOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={createHost.isPending}>
              Create host
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
function hostCountLabel(loaded: number, more: boolean): string {
  if (more) return `${loaded} hosts loaded, more available`;
  return `${loaded} ${loaded === 1 ? 'host' : 'hosts'}`;
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
