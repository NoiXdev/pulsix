import { useState } from "react";
import { useParams } from "@tanstack/react-router";
import { Lock, Server } from "lucide-react";
import { Card, GridHeader, GridRow } from "../../ui/table.tsx";
import { Button } from "../../ui/button.tsx";
import { Select } from "../../ui/Select.tsx";
import { useToast } from "../../ui/toastContext.ts";
import { useQueryGuard } from "../../ui/useQueryGuard.tsx";
import { QueryLoading } from "../../ui/QueryState.tsx";
import {
  useBastions,
  useClearBastionKey,
  useDeleteBastion,
  useSetBastionKey,
  useSetZoneBastion,
} from "../../api/hooks/useSsh.ts";
import { useCredentials } from "../../api/hooks/useCredentials.ts";
import { useOrg } from "../../api/hooks/useOrgs.ts";
import { useZones } from "../../api/hooks/useHosts.ts";
import { may } from "../../lib/roles.ts";
import type { Bastion } from "../../api/types.ts";
import { BastionDialog } from "./BastionDialog.tsx";
import { PinStateBadge } from "../hosts/PinStateBadge.tsx";
import { Pill, type Tone } from "../../ui/badge.tsx";
import { useT } from "../../i18n/useT.ts";

const COLUMNS =
  "minmax(0, 1fr) minmax(0, 1.2fr) 130px 130px minmax(0, 1.6fr) 120px";

const KEY_STATE_TONE: Record<string, Tone> = {
  pinned: "ok",
  changed: "crit",
  unpinned: "warn",
};

export function JumpHostsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const toast = useToast();
  const bastions = useBastions(org);
  const credentials = useCredentials(org);
  const zones = useZones(org);
  const orgInfo = useOrg(org);
  const remove = useDeleteBastion(org);
  const setZone = useSetZoneBastion(org);
  const [dialog, setDialog] = useState<{ bastion?: Bastion } | null>(null);

  const guard = useQueryGuard(bastions);
  if (guard) return guard;
  if (!bastions.data) return <QueryLoading />;
  const list = bastions.data.bastions ?? [];
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

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t.credentials.jumpHosts.title}</h1>
        {/* Gated on the same canWrite the host-key actions below already
            use (backlog 1.48). The server enforces this either way, so
            the point is not the authorization — it is that a reader was
            being offered three controls that could only ever answer 403. */}
        {canWrite && (
          <Button variant="primary" onClick={() => setDialog({})}>
            <Server size={15} /> {t.credentials.jumpHosts.add}
          </Button>
        )}
      </div>
      <span className="text-[13px] text-muted-foreground">
        {t.credentials.jumpHosts.resolution}
      </span>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.credentials.jumpHosts.columns.name}</span>
          <span>{t.credentials.jumpHosts.columns.login}</span>
          <span>{t.credentials.jumpHosts.columns.reached}</span>
          <span>{t.credentials.jumpHosts.columns.credential}</span>
          <span>{t.credentials.jumpHosts.columns.hostKey}</span>
          <span />
        </GridHeader>
        {list.map((b) => (
          <GridRow key={b.id} columns={COLUMNS}>
            <span className="font-mono text-[13px]">{b.name}</span>
            <span className="truncate font-mono text-xs">{`${b.username}@${b.address}:${b.port}`}</span>
            <span className="text-xs text-muted-foreground">
              {b.via_bastion_id
                ? t.credentials.jumpHosts.via(bastionName(b.via_bastion_id))
                : t.credentials.jumpHosts.directly}
            </span>
            <span className="truncate font-mono text-xs text-muted-foreground">
              {creds.find((c) => c.id === b.credential_id)?.name ?? "—"}
            </span>
            <BastionHostKey org={org} bastion={b} canWrite={canWrite} />
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button
                    variant="ghost"
                    aria-label={t.credentials.jumpHosts.editLabel(b.name)}
                    onClick={() => setDialog({ bastion: b })}
                  >
                    {t.credentials.jumpHosts.edit}
                  </Button>
                  <Button
                    variant="ghost"
                    aria-label={t.credentials.jumpHosts.deleteLabel(b.name)}
                    onClick={async () => {
                      if (!confirm(t.credentials.jumpHosts.confirmDelete(b.name))) return;
                      try {
                        await remove.mutateAsync(b.id);
                        toast.show(t.credentials.jumpHosts.deleted(b.name));
                      } catch (err) {
                        toast.show(
                          err instanceof Error
                            ? err.message
                            : t.credentials.jumpHosts.deleteFailed,
                          "destructive",
                        );
                      }
                    }}
                  >
                    {t.common.delete}
                  </Button>
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && (
          <p className="px-4 py-6 text-center text-xs text-muted-foreground">
            {t.credentials.jumpHosts.empty}
          </p>
        )}
      </Card>

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

/**
 * BastionHostKey is the jump host's TOFU cell (backlog 2.40), mirroring the
 * host card's key block: the pinned fingerprint (or the newest observation),
 * the pin-state badge and the pin/unpin actions — a `changed` key fails
 * every connection through the jump host until the new key is pinned and a
 * fresh connection test confirms it.
 */
function BastionHostKey({
  org,
  bastion,
  canWrite,
}: {
  org: string;
  bastion: Bastion;
  canWrite: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const pin = useSetBastionKey(org, bastion.id);
  const unpin = useClearBastionKey(org, bastion.id);

  const state =
    bastion.observed_state ?? (bastion.ssh_host_key ? "pinned" : "");
  const fingerprint = bastion.ssh_host_key ?? bastion.observed_key ?? "";
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <Lock size={12} className="text-muted-foreground" />
        <span className="truncate font-mono text-[11px]" title={fingerprint}>
          {fingerprint || t.hosts.connection.noKey}
        </span>
        {state && (
          <Pill tone={KEY_STATE_TONE[state] ?? "unknown"}>
            {(t.hosts.connection.keyState as Record<string, string>)[state] ?? state}
          </Pill>
        )}
        <PinStateBadge state={bastion.pin_state} />
      </div>
      {canWrite && state && state !== "pinned" && (
        <Button
          type="button"
          variant="ghost"
          className="h-6 px-2 text-[11px]"
          disabled={pin.isPending || !bastion.observed_key}
          aria-label={t.credentials.jumpHosts.pinLabel(bastion.name)}
          onClick={async () => {
            if (!bastion.observed_key) return;
            try {
              await pin.mutateAsync(bastion.observed_key);
              toast.show(t.credentials.jumpHosts.pinned(bastion.name));
            } catch (err) {
              toast.show(
                err instanceof Error ? err.message : t.hosts.connection.pinFailed,
                "destructive",
              );
            }
          }}
        >
          {t.credentials.jumpHosts.pin}
        </Button>
      )}
      {canWrite && state === "pinned" && (
        <Button
          type="button"
          variant="ghost"
          className="h-6 px-2 text-[11px]"
          disabled={unpin.isPending}
          aria-label={t.credentials.jumpHosts.unpinLabel(bastion.name)}
          onClick={async () => {
            try {
              await unpin.mutateAsync();
              toast.show(t.credentials.jumpHosts.pinCleared);
            } catch (err) {
              toast.show(
                err instanceof Error ? err.message : t.hosts.connection.unpinFailed,
                "destructive",
              );
            }
          }}
        >
          {t.hosts.connection.unpin}
        </Button>
      )}
      {bastion.pin_state === "fingerprint_only" && (
        <span className="text-[11px] text-muted-foreground">
          {t.hosts.pin.needsConfirmation}
        </span>
      )}
      {state === "changed" && (
        <span className="text-[11px]" style={{ color: "var(--px-crit)" }}>
          {t.credentials.jumpHosts.keyChanged}
        </span>
      )}
    </div>
  );
}
