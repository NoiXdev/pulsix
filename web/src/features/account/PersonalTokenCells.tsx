import type { PersonalGrant } from '../../api/hooks/usePersonalTokens.ts';
import type { Messages } from '../../i18n/types.ts';
import { roleLabel } from '../../i18n/roleLabel.ts';

/** One organization of a personal token in its list: "acme: Operator", and
 * whether an administrator blocked it or the organization turned personal
 * tokens off. */
export function GrantLine({ grant, t }: { grant: PersonalGrant; t: Messages }) {
  const p = t.personalTokens;
  return (
    <span className={grant.state === 'active' ? undefined : 'text-muted-foreground'} title={grant.state === 'off' ? p.grantOffHint : undefined}>
      {p.grant(grant.org_slug, roleLabel(t, grant.role))}
      {grant.state === 'blocked' && <span className="text-destructive"> · {p.grantBlocked}</span>}
      {grant.state === 'off' && <span> · {p.grantOff}</span>}
    </span>
  );
}
