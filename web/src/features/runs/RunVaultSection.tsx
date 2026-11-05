import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import type { JobVaultIdentity } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * RunVaultSection says which labelled vault passwords the run received, and
 * whether each came from the repository or the template (decision 2's
 * template-wins precedence, internal/repos/vault.go's
 * ResolveVaultIdentities).
 *
 * `recorded` means this is what the claim actually resolved and stored for
 * this run (backlog 2.91); otherwise there is no stored record — a run still
 * queued, one claimed before that recording existed, or a job cancelled
 * before it was ever claimed — so this is a live read of the repository's
 * and template's configuration right now, which may no longer match what
 * actually ran if either changed since. The section header says which.
 */
export function RunVaultSection({ identities, recorded }: { identities: JobVaultIdentity[]; recorded: boolean }) {
  const t = useT();
  return (
    <Section
      id="vault"
      title={t.credentials.vault.heading}
      actions={
        <span className="text-[11px] text-muted-foreground" title={recorded ? t.runs.detail.recordedTitle : t.runs.detail.computedTitle}>
          {recorded ? t.runs.detail.recorded : t.runs.detail.computed}
        </span>
      }
    >
      <Card>
        <ul className="flex flex-col gap-1 p-4 text-xs" aria-label={t.credentials.vault.heading}>
          {identities.map((identity) => (
            <li key={identity.label} className="flex items-center justify-between gap-2">
              <span className="font-mono">{identity.label}</span>
              <span className="text-[11px] text-muted-foreground">{identity.from_template ? t.runs.detail.fromTemplate : t.runs.detail.fromRepository}</span>
            </li>
          ))}
        </ul>
      </Card>
    </Section>
  );
}
