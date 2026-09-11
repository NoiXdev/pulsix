import { Terminal, Activity } from 'lucide-react';

export type CapabilityKind = 'managed' | 'monitored';
export type CapabilityPatch = { managed: boolean } | { monitored: boolean };

export interface CapabilityCellProps {
  kind: CapabilityKind;
  on: boolean;
  /** A short human-readable detail shown alongside the capability's label
   * when it is on — e.g. "user@ · key" for managed (SSH user and key
   * name) or a check count for monitored. Omit it when there is nothing
   * more specific to say. */
  detail?: string;
  onChange: (patch: CapabilityPatch) => void;
}

const LABEL: Record<CapabilityKind, string> = { managed: 'Ansible', monitored: 'Agent' };
const ICON: Record<CapabilityKind, typeof Terminal> = { managed: Terminal, monitored: Activity };

/**
 * CapabilityCell renders one cell of the hosts table's Managed/Monitored
 * columns (design/mockups/build.mjs, HOST LIST's `cap()` generator):
 * an icon and label when the capability is on, or a dashed "off · enable"
 * affordance when it is off. It never toggles the *other* capability —
 * clicking "enable" always sends a patch naming only this cell's own
 * `kind`, matching PATCH …/hosts/{id}/capabilities' independent-fields
 * contract (a field not present in the request body is left untouched,
 * not reset to false).
 */
export function CapabilityCell({ kind, on, detail, onChange }: CapabilityCellProps) {
  const Icon = ICON[kind];

  if (on) {
    return (
      <div className="flex items-center gap-2 min-w-0">
        <span className="flex h-[22px] w-[22px] shrink-0 items-center justify-center rounded-md bg-brand/10 text-brand">
          <Icon size={13} />
        </span>
        <span className="truncate text-xs font-semibold">
          {detail ? `${LABEL[kind]} · ${detail}` : LABEL[kind]}
        </span>
      </div>
    );
  }

  return (
    <button
      type="button"
      className="flex items-center gap-2 text-muted-foreground"
      onClick={() => onChange(kind === 'managed' ? { managed: true } : { monitored: true })}
    >
      <span className="flex h-[22px] w-[22px] shrink-0 items-center justify-center rounded-md border border-dashed border-border">
        <Icon size={13} />
      </span>
      <span className="text-xs">
        off <span className="text-brand-dim">· enable</span>
      </span>
    </button>
  );
}
