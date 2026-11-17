import { Pill } from '../../ui/badge.tsx';
import type { PersonalTokenEntry } from '../../api/hooks/usePersonalTokens.ts';
import type { Messages } from '../../i18n/types.ts';

/** The states of a personal entry, shown under its name. */
export function PersonalState({ row, t }: { row: PersonalTokenEntry; t: Messages }) {
  const p = t.tokens.personal;
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      {row.blocked ? (
        <span title={p.blockedHint}>
          <Pill tone="crit">{p.blocked}</Pill>
          <span className="sr-only">. {p.blockedHint}</span>
        </span>
      ) : (
        row.owner_blocked && (
          <span title={p.ownerBlockedHint}>
            <Pill tone="warn">{p.ownerBlocked}</Pill>
            <span className="sr-only">. {p.ownerBlockedHint}</span>
          </span>
        )
      )}
      {row.token_revoked && (
        <span title={p.tokenRevokedHint}>
          <Pill tone="unknown">{p.tokenRevoked}</Pill>
          <span className="sr-only">. {p.tokenRevokedHint}</span>
        </span>
      )}
    </span>
  );
}
