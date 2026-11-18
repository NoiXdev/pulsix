import { useState } from "react";
import { useParams } from "@tanstack/react-router";
import { Server } from "lucide-react";
import { Card, GridRow } from "../../ui/table.tsx";
import { Button } from "../../ui/button.tsx";
import { DataList } from "../../ui/data-list/index.ts";
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from "../../ui/list-toolbar.tsx";
import { Select } from "../../ui/Select.tsx";
import { useToast } from "../../ui/toastContext.ts";
import { useQueryGuard } from "../../ui/useQueryGuard.tsx";
import { QueryLoading } from "../../ui/QueryState.tsx";
import { useBastions, useSetZoneBastion } from "../../api/hooks/useSsh.ts";
import { useCredentials } from "../../api/hooks/useCredentials.ts";
import { useOrg } from "../../api/hooks/useOrgs.ts";
import { useZones } from "../../api/hooks/useHosts.ts";
import { may } from "../../lib/roles.ts";
import type { Bastion } from "../../api/types.ts";
import { FilterSelect } from "../lists/FilterSelect.tsx";
import { useListFilters } from "../lists/filterSearch.ts";
import { useListView } from "../lists/useListView.ts";
import { BastionDialog } from "./BastionDialog.tsx";
import { bastionColumns } from "./bastionColumns.tsx";
import { BASTION_KEY_STATES, filterBastions, JUMP_HOST_FILTERS } from "./jumpHostsSearch.ts";
import { useBastionListActions } from "./useBastionListActions.tsx";
import { useT } from "../../i18n/useT.ts";

/** JumpHostsPage lists the organization's jump hosts (list id `jump-hosts`,
 * one request, sorted and paged in the browser) and the zone defaults. A
 * writer's row menu edits, deletes and pins; a reader gets the read-only
 * list. */
export function JumpHostsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const toast = useToast();
  const bastions = useBastions(org);
  const credentials = useCredentials(org);
  const zones = useZones(org);
  const orgInfo = useOrg(org);
  const setZone = useSetZoneBastion(org);
  const [dialog, setDialog] = useState<{ bastion?: Bastion } | null>(null);

  const list = bastions.data?.bastions ?? [];
  // A viewer may read jump hosts but not credentials; names fall back to "—".
  const creds = credentials.data?.credentials ?? [];
  const bastionName = (id?: string | null) =>
    list.find((b) => b.id === id)?.name ?? t.credentials.jumpHosts.directly;
  // Pinning is inventory.write, the same gate the host card mirrors.
  const canWrite = may(orgInfo.data?.org.role, "inventory.write");
  // Gates BastionDialog's own Credential create row: credentials.write is
  // admin, stricter than the inventory.write that gates jump host writes
  // on this page itself (v0.4g Task 7 fix round).
  const canCreateCredential = may(orgInfo.data?.org.role, "credentials.write");

  const columns = bastionColumns({
    t,
    bastionName,
    credentialName: (id) => creds.find((c) => c.id === id)?.name ?? "—",
  });
  const { filters, active, setFilter, reset } = useListFilters(JUMP_HOST_FILTERS);
  const view = useListView("jump-hosts", columns, { mode: "client", rowKey: (b) => b.id, resetKey: JSON.stringify(filters) });
  const rowActions = useBastionListActions(org, canWrite, (bastion) => setDialog({ bastion }));

  const guard = useQueryGuard(bastions);
  if (guard) return guard;
  if (!bastions.data) return <QueryLoading />;
  const shown = filterBastions(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const f = t.credentials.jumpHosts.filters;
  const keyStates = t.hosts.connection.keyState;
  const credentialOptions = creds.map((c) => ({ value: c.id, label: c.name }));
  // A credential only the URL names (a stale link) stays pickable so it can be cleared.
  if (filters.credential && !creds.some((c) => c.id === filters.credential)) {
    credentialOptions.push({ value: filters.credential, label: filters.credential.slice(0, 8) });
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t.credentials.jumpHosts.title}</h1>
        {/* Gated on the same canWrite the row menu uses (backlog 1.48). The
            server enforces this either way, so the point is not the
            authorization — it is that a reader was being offered controls
            that could only ever answer 403. */}
        {canWrite && (
          <Button variant="primary" onClick={() => setDialog({})}>
            <Server size={15} /> {t.credentials.jumpHosts.add}
          </Button>
        )}
      </div>
      <span className="text-[13px] text-muted-foreground">
        {t.credentials.jumpHosts.resolution}
      </span>
      <DataList
        label={t.credentials.jumpHosts.label}
        columns={columns}
        rows={rows}
        rowKey={(b) => b.id}
        rowActions={rowActions}
        rowLabel={(b) => b.name}
        loading={!view.ready}
        empty={active ? t.credentials.jumpHosts.noMatch : t.credentials.jumpHosts.empty}
        toolbar={
          <ListToolbar
            count={<ResultCount active={active} shown={shown.length} total={list.length} noun={t.credentials.jumpHosts.count} />}
          >
            <SearchInput
              label={t.credentials.jumpHosts.search}
              placeholder={t.credentials.jumpHosts.filterPlaceholder}
              value={filters.q ?? ""}
              onChange={(q) => setFilter("q", q || undefined, { replace: true })}
            />
            <FilterSelect
              label={f.keyState}
              allLabel={f.anyKeyState}
              options={BASTION_KEY_STATES.map((k) => ({ value: k, label: keyStates[k] }))}
              value={filters.key}
              onChange={(v) => setFilter("key", v as typeof filters.key)}
            />
            {(creds.length > 0 || filters.credential) && (
              <FilterSelect
                label={f.credential}
                allLabel={f.allCredentials}
                options={credentialOptions}
                value={filters.credential}
                onChange={(v) => setFilter("credential", v)}
              />
            )}
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.credentials.jumpHosts.count)}
      />

      <h2 className="text-[15px] font-semibold">{t.credentials.jumpHosts.zoneDefaults}</h2>
      <Card>
        {(zones.data?.zones ?? []).map((z) => (
          <GridRow key={z.id} columns="minmax(0, 1fr) 260px">
            <label htmlFor={`zone-${z.id}`} className="text-[13px]">
              {z.name}{" "}
              <span className="font-mono text-xs text-muted-foreground">
                {z.slug}
              </span>
            </label>
            {/* No create row: this page's own "Add jump host" button sits
                right above this table — a second way to create one here
                would just duplicate it (v0.4g Move 3). */}
            <Select
              id={`zone-${z.id}`}
              options={list.map((b) => ({ value: b.id, label: b.name }))}
              value={z.bastion_id ?? ""}
              onChange={async (value) => {
                try {
                  await setZone.mutateAsync({
                    zoneId: z.id,
                    bastionId: value || undefined,
                  });
                  toast.show(t.credentials.jumpHosts.zoneSaved(z.name));
                } catch (err) {
                  toast.show(
                    err instanceof Error ? err.message : t.credentials.jumpHosts.saveFailed,
                    "destructive",
                  );
                }
              }}
              placeholder={t.credentials.jumpHosts.zoneNone}
            />
          </GridRow>
        ))}
      </Card>
      {dialog && (
        <BastionDialog
          org={org}
          bastion={dialog.bastion}
          bastions={list}
          credentials={creds}
          canCreateCredential={canCreateCredential}
          onClose={() => setDialog(null)}
          // This page's own list refetches from useCreateBastion's own
          // invalidation; nothing here needs the new id for itself.
          onCreated={() => undefined}
        />
      )}
    </>
  );
}
