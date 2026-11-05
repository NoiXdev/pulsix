import { useNavigate } from '@tanstack/react-router';
import { Pill } from '../../ui/badge.tsx';
import { ListRow, RowMenu, RowMenuItem, StaticListRow } from '../../ui/list-row.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import type { Agent, AgentToken } from '../../api/types.ts';
import { VersionSkewBadge } from '../workers/VersionSkewBadge.tsx';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from '../workers/expiry.ts';

/** AgentRow is one host agent. The host's page is the agent's detail page, so
 * the whole row opens it at its Connection section, where the Agent card is.
 * The ⋯ menu offers Open host and, for a writer, Revoke…; `columns` reserves
 * the menu's 40px as its last track, left empty here. */
export function AgentRow({
  org, agent: a, columns, now, canWrite, onRevoke,
}: {
  org: string;
  agent: Agent;
  columns: string;
  now: Date;
  canWrite: boolean;
  onRevoke: (a: Agent) => void;
}) {
  const t = useT();
  const navigate = useNavigate();
  const expiring = !!a.cert_expires_at && daysLeft(a.cert_expires_at, now) < EXPIRY_WARN_DAYS;
  const statusLabel = a.status in t.agents.status ? t.agents.status[a.status as keyof typeof t.agents.status] : a.status;
  return (
    <ListRow
      to="/o/$org/hosts/$id"
      params={{ org, id: a.host_id }}
      hash="connection"
      columns={columns}
      label={a.host_name}
      menu={
        <RowMenu label={t.common.rowActions(a.host_name)}>
          <RowMenuItem onSelect={() => void navigate({ to: '/o/$org/hosts/$id', params: { org, id: a.host_id }, hash: 'connection' })}>{t.agents.openHost}</RowMenuItem>
          {canWrite && (
            <RowMenuItem onSelect={() => onRevoke(a)} destructive>
              {t.agents.revokeItem}
            </RowMenuItem>
          )}
        </RowMenu>
      }
    >
      <span className="truncate text-[13px]" data-row>
        {a.host_name}
      </span>
      <span className="truncate font-mono text-xs">{a.agent_name}</span>
      <span>
        <Pill tone={a.status === 'online' ? 'ok' : 'warn'}>{statusLabel}</Pill>
      </span>
      <span className={`text-xs ${a.status === 'online' ? 'text-muted-foreground' : 'text-[var(--px-warn)]'}`}>{seenLabel(a.last_seen, now, t)}</span>
      <span className="flex min-w-0 items-center gap-2 text-xs">
        <span className="truncate font-mono">{a.version || '—'}</span>
        <VersionSkewBadge status={a.version_status} />
      </span>
      <span className={`text-xs ${expiring ? 'font-semibold text-[var(--px-warn)]' : 'text-muted-foreground'}`} data-expiring={expiring || undefined}>
        {expiryLabel(a.cert_expires_at, now, t)}
      </span>
      <span />
    </ListRow>
  );
}

/** AgentTokenRow is one pending enrollment token; a writer's ⋯ menu offers
 * Withdraw. */
export function AgentTokenRow({
  token: tok, columns, canWrite, onWithdraw,
}: {
  token: AgentToken;
  columns: string;
  canWrite: boolean;
  onWithdraw: (tok: AgentToken) => void;
}) {
  const t = useT();
  const fmt = useFormat();
  const name = tok.name_hint || tok.id.slice(0, 8);
  return (
    <StaticListRow
      columns={columns}
      menu={
        canWrite && (
          <RowMenu label={t.common.rowActions(name)}>
            <RowMenuItem onSelect={() => onWithdraw(tok)}>{t.agents.withdraw}</RowMenuItem>
          </RowMenu>
        )
      }
    >
      <span className="truncate font-mono text-[13px]">{name}</span>
      <span className="text-xs text-muted-foreground">
        {tok.uses} / {tok.max_uses}
      </span>
      <span className="text-xs text-muted-foreground">{tok.host_id ? t.agents.boundToHost : t.agents.boundNewHost}</span>
      <span className="text-xs text-muted-foreground">{fmt.dateTime(tok.expires_at)}</span>
      {canWrite && <span />}
    </StaticListRow>
  );
}
